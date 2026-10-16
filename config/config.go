// Package config builds notijet providers from configuration — a TOML
// section, environment, or any map of settings — so which platform sends a
// channel's messages is a deployment decision rather than code.
//
//	[notijet.providers.otp]
//	type    = "kavenegar"
//	api_key = "env:KAVENEGAR_API_KEY"
//	sender  = "10004346"
//	retry_attempts = 3
//
//	[notijet.providers.sms]
//	type      = "failover"
//	providers = "otp, smsir"
//	default   = true
//
// Each spec names a provider type registered in a Registry (see
// config/builtin for the built-in providers) and carries its settings. A few
// keys are understood for every type:
//
//	type            the provider type (required)
//	default         true makes it the default of its channel in a Notifier
//	retry_attempts  wraps it in notijet.Retry with that many attempts
//	retry_initial   first retry delay (Go duration, default 1s)
//	retry_max       longest retry delay (default 30s)
//
// The "failover" type composes the providers named in its providers list,
// which may themselves be failovers. A setting value may be a secret
// reference instead of the secret: "env:NAME" reads an environment variable,
// "file:/path" reads a file (trailing newlines trimmed), and other prefixes
// go to the Resolver set on the Builder (a microjet secretx.Resolver fits).
// A key the provider type does not use is an error, so a typo cannot
// silently drop a setting.
package config

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hatami57/notijet"
)

// Spec is the configuration of one provider.
type Spec struct {
	// Name is the name the provider is registered under in a Notifier.
	Name string
	// Settings are the provider's settings, "type" included.
	Settings map[string]string
}

// Factory builds a provider of one type from its settings.
type Factory func(ctx context.Context, s *Settings) (notijet.Provider, error)

// Registry maps provider types to factories.
type Registry struct {
	factories map[string]Factory
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry { return &Registry{factories: map[string]Factory{}} }

// Register adds a factory for typ, replacing any earlier one. Types are
// case-insensitive.
func (r *Registry) Register(typ string, f Factory) *Registry {
	r.factories[strings.ToLower(typ)] = f
	return r
}

// Types returns the registered types, sorted.
func (r *Registry) Types() []string { return slices.Sorted(maps.Keys(r.factories)) }

// Resolver resolves a secret reference — a setting value with a prefix other
// than "env:" and "file:" — to the secret.
type Resolver func(ctx context.Context, ref string) (string, error)

// Builder turns specs into providers.
type Builder struct {
	Registry *Registry
	// Resolve, when set, resolves setting values of the form "<scheme>:<ref>"
	// whose scheme is listed in Schemes.
	Resolve Resolver
	// Schemes are the prefixes (without ":") handled by Resolve, e.g.
	// "secret". Values with other prefixes are taken literally.
	Schemes []string
}

// Built is the outcome of Build.
type Built struct {
	// Providers holds each spec's provider, wrapped as its settings ask, in
	// the order of the specs.
	Providers []Named
	// Defaults are the names of the providers marked default.
	Defaults []string
}

// Named is a provider and the name to register it under.
type Named struct {
	Name     string
	Provider notijet.Provider
}

// Register adds the built providers to n and applies the defaults.
func (b *Built) Register(n *notijet.Notifier) error {
	for _, p := range b.Providers {
		if err := n.Register(p.Name, p.Provider); err != nil {
			return err
		}
	}
	for _, name := range b.Defaults {
		if err := n.SetDefault(name); err != nil {
			return err
		}
	}
	return nil
}

// Build builds every spec. Failovers may refer to any other spec by name,
// in any order; a reference to an unknown name or a cycle is an error. All
// errors are reported together.
func (b *Builder) Build(ctx context.Context, specs []Spec) (*Built, error) {
	if b == nil || b.Registry == nil {
		return nil, invalid("", "provider registry is nil")
	}
	byName := map[string]Spec{}
	for _, s := range specs {
		if s.Name == "" {
			return nil, invalid("", "a provider spec has no name")
		}
		if _, dup := byName[s.Name]; dup {
			return nil, invalid(s.Name, "defined twice")
		}
		byName[s.Name] = s
	}
	built := map[string]notijet.Provider{}
	failed := map[string]error{}
	building := map[string]bool{}
	var errs []error
	out := &Built{}
	var build func(name string) (notijet.Provider, error)
	build = func(name string) (notijet.Provider, error) {
		if p, ok := built[name]; ok {
			return p, nil
		}
		if err, ok := failed[name]; ok {
			return nil, err
		}
		spec, ok := byName[name]
		if !ok {
			return nil, invalid(name, "no provider with this name is configured")
		}
		if building[name] {
			return nil, invalid(name, "failover cycle")
		}
		building[name] = true
		defer delete(building, name)
		p, err := b.buildOne(ctx, spec, build)
		if err != nil {
			failed[name] = err
			return nil, err
		}
		built[name] = p
		return p, nil
	}
	for _, s := range specs {
		p, err := build(s.Name)
		if err != nil {
			// A failover fails with its member's error; report each once.
			duplicate := false
			for _, previous := range errs {
				if errors.Is(previous, err) || previous.Error() == err.Error() {
					duplicate = true
					break
				}
			}
			if !duplicate {
				errs = append(errs, err)
			}
			continue
		}
		out.Providers = append(out.Providers, Named{s.Name, p})
		if isTrue(s.Settings["default"]) {
			out.Defaults = append(out.Defaults, s.Name)
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return out, nil
}

func (b *Builder) buildOne(ctx context.Context, spec Spec, build func(string) (notijet.Provider, error)) (notijet.Provider, error) {
	values := map[string]string{}
	for k, v := range spec.Settings {
		k = strings.ToLower(strings.TrimSpace(k))
		resolved, err := b.resolve(ctx, v)
		if err != nil {
			return nil, invalid(spec.Name, k+": "+err.Error())
		}
		values[k] = resolved
	}
	s := &Settings{name: spec.Name, values: values, used: map[string]bool{"type": true, "default": true}}
	typ := strings.ToLower(s.String("type"))
	attempts := s.Int("retry_attempts")
	initial := s.Duration("retry_initial")
	maxDelay := s.Duration("retry_max")

	var p notijet.Provider
	var err error
	switch typ {
	case "":
		return nil, invalid(spec.Name, "type is required")
	case "failover":
		var members []notijet.Provider
		for _, name := range s.List("providers") {
			m, err := build(name)
			if err != nil {
				return nil, err
			}
			members = append(members, m)
		}
		if len(members) == 0 && s.Err() == nil {
			return nil, invalid(spec.Name, "providers is required for a failover")
		}
		if s.Err() == nil {
			p = notijet.Failover(members...)
		}
	default:
		f := b.Registry.factories[typ]
		if f == nil {
			return nil, invalid(spec.Name, "unknown provider type "+strconv.Quote(typ)+" (known: "+strings.Join(b.Registry.Types(), ", ")+")")
		}
		p, err = f(ctx, s)
	}

	if err := errors.Join(err, s.Err()); err != nil {
		return nil, wrap(spec.Name, err)
	}
	if attempts > 1 {
		opts := []notijet.RetryOption{notijet.RetryAttempts(attempts)}
		if initial > 0 || maxDelay > 0 {
			if initial == 0 {
				initial = notijet.DefaultRetryInitial
			}
			if maxDelay == 0 {
				maxDelay = max(notijet.DefaultRetryMax, initial)
			}
			opts = append(opts, notijet.RetryBackoff(initial, maxDelay))
		}
		p = notijet.Retry(p, opts...)
	}
	return p, nil
}

// resolve replaces a secret reference with the secret.
func (b *Builder) resolve(ctx context.Context, v string) (string, error) {
	scheme, ref, ok := strings.Cut(v, ":")
	if !ok {
		return v, nil
	}
	switch scheme {
	case "env":
		val, ok := os.LookupEnv(ref)
		if !ok {
			return "", errors.New("environment variable " + ref + " is not set")
		}
		return val, nil
	case "file":
		data, err := os.ReadFile(ref)
		if err != nil {
			return "", err
		}
		return strings.TrimRight(string(data), "\r\n"), nil
	}
	if b.Resolve != nil && slices.Contains(b.Schemes, scheme) {
		return b.Resolve(ctx, v)
	}
	return v, nil
}

// Settings gives a factory typed access to a spec's settings. It remembers
// which keys were read, so keys nobody read are reported by Err, and it
// collects parse errors so a factory can read everything and check once.
type Settings struct {
	name   string
	values map[string]string
	used   map[string]bool
	errs   []error
}

// NewSettings returns Settings over values, for testing factories.
func NewSettings(name string, values map[string]string) *Settings {
	return &Settings{name: name, values: values, used: map[string]bool{"type": true, "default": true}}
}

// Name returns the spec's name.
func (s *Settings) Name() string { return s.name }

func (s *Settings) get(key string) (string, bool) {
	s.used[key] = true
	v, ok := s.values[key]
	return strings.TrimSpace(v), ok && strings.TrimSpace(v) != ""
}

// String returns the value of key, or "".
func (s *Settings) String(key string) string {
	v, _ := s.get(key)
	return v
}

// Required returns the value of key, recording an error when it is missing.
func (s *Settings) Required(key string) string {
	v, ok := s.get(key)
	if !ok {
		s.errs = append(s.errs, errors.New(key+" is required"))
	}
	return v
}

// Bool returns key as a boolean (true/false, yes/no, 1/0, on/off), false when
// unset.
func (s *Settings) Bool(key string) bool {
	v, ok := s.get(key)
	if !ok {
		return false
	}
	switch strings.ToLower(v) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	s.errs = append(s.errs, fmt.Errorf("%s: %q is not a boolean", key, v))
	return false
}

// Int returns key as an integer, 0 when unset.
func (s *Settings) Int(key string) int {
	v, ok := s.get(key)
	if !ok {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		s.errs = append(s.errs, fmt.Errorf("%s: %q is not an integer", key, v))
	}
	return n
}

// Duration returns key as a Go duration ("30s", "2m"), 0 when unset.
func (s *Settings) Duration(key string) time.Duration {
	v, ok := s.get(key)
	if !ok {
		return 0
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		s.errs = append(s.errs, fmt.Errorf("%s: %q is not a duration like 30s", key, v))
	}
	return d
}

// List returns key split on commas, entries trimmed, blanks dropped.
func (s *Settings) List(key string) []string {
	v, _ := s.get(key)
	return notijet.CleanList(strings.Split(v, ","))
}

// OneOf returns key, recording an error when it is set to a value not in
// allowed (compared case-insensitively; the result is lower-cased).
func (s *Settings) OneOf(key string, allowed ...string) string {
	v, ok := s.get(key)
	if !ok {
		return ""
	}
	v = strings.ToLower(v)
	valid := false
	for _, candidate := range allowed {
		if strings.ToLower(candidate) == v {
			valid = true
			break
		}
	}
	if !valid {
		s.errs = append(s.errs, fmt.Errorf("%s: %q is not one of %s", key, v, strings.Join(allowed, ", ")))
	}
	return v
}

// Prefixed returns the settings whose keys start with prefix, keyed by the
// rest of the key — e.g. Prefixed("header.") for "header.authorization".
func (s *Settings) Prefixed(prefix string) map[string]string {
	out := map[string]string{}
	for k, v := range s.values {
		if rest, ok := strings.CutPrefix(k, prefix); ok && rest != "" {
			s.used[k] = true
			out[rest] = v
		}
	}
	return out
}

// Errorf records a factory-specific error.
func (s *Settings) Errorf(format string, args ...any) {
	s.errs = append(s.errs, fmt.Errorf(format, args...))
}

// Err returns the errors recorded so far plus one for every key nobody read.
func (s *Settings) Err() error {
	errs := slices.Clone(s.errs)
	for _, k := range slices.Sorted(maps.Keys(s.values)) {
		if !s.used[k] {
			errs = append(errs, errors.New("unknown setting "+k))
		}
	}
	return errors.Join(errs...)
}

func isTrue(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func invalid(name, message string) error {
	if name != "" {
		message = "provider " + name + ": " + message
	}
	return notijet.Invalid("config", message)
}

func wrap(name string, err error) error {
	return invalid(name, strings.ReplaceAll(err.Error(), "\n", "; "))
}
