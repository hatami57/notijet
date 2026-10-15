package module

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/hatami57/microjet/core/configx"
	"github.com/hatami57/microjet/core/secretx"

	"github.com/hatami57/notijet"
	"github.com/hatami57/notijet/config"
)

// ConfigSection is the config key holding one table per provider:
//
//	[notijet.providers.otp]
//	type    = "kavenegar"
//	api_key = "secret:prod/kavenegar"
//	default = true
//
// Each table's name is the provider's name in the Notifier and its keys are
// the settings described by the config and config/builtin packages. A nested
// table becomes dotted keys ([notijet.providers.hook.header] Authorization =
// "..." is header.authorization).
const ConfigSection = "notijet.providers"

// WithConfig builds providers from the ConfigSection of the app config with
// the factories in registry — config/builtin's Registry() for the built-in
// providers — and registers them in the Notifier after those added with
// WithProvider. Configure the Notifier's defaults with `default = true` in a
// table or with WithDefault.
func WithConfig(registry *config.Registry) Option {
	return func(c *options) { c.registry = registry }
}

// WithSecrets resolves setting values written as "<scheme>:<reference>"
// through resolver, for each of schemes (default "secret"), so config files
// hold references to secrets rather than the secrets.
func WithSecrets(resolver secretx.Resolver, schemes ...string) Option {
	if len(schemes) == 0 {
		schemes = []string{"secret"}
	}
	return func(c *options) { c.resolver, c.schemes = resolver, schemes }
}

// configService builds the configured providers when the host reads config.
type configService struct {
	cfg      *options
	notifier *notijet.Notifier
}

// ReadConfig implements configx.Configurable.
func (s *configService) ReadConfig(r configx.Reader) error {
	specs := Specs(r.ReadMap(ConfigSection))
	b := &config.Builder{Registry: s.cfg.registry, Schemes: s.cfg.schemes}
	if res := s.cfg.resolver; res != nil {
		b.Resolve = func(ctx context.Context, value string) (string, error) {
			_, ref, _ := strings.Cut(value, ":")
			v, err := res.Resolve(ctx, ref)
			return v.Reveal(), err
		}
	}
	built, err := b.Build(context.Background(), specs)
	if err != nil {
		return err
	}
	if err := built.Register(s.notifier); err != nil {
		return err
	}
	return applyDefaults(s.notifier, s.cfg.defaults)
}

// Specs converts a config section (provider name → table) into specs, in
// name order. Scalar values are formatted as strings and nested tables
// flattened to dotted keys.
func Specs(section map[string]any) []config.Spec {
	var specs []config.Spec
	for _, name := range slices.Sorted(maps.Keys(section)) {
		table, ok := section[name].(map[string]any)
		if !ok {
			specs = append(specs, config.Spec{Name: name, Settings: map[string]string{"type": ""}})
			continue
		}
		settings := map[string]string{}
		flatten("", table, settings)
		specs = append(specs, config.Spec{Name: name, Settings: settings})
	}
	return specs
}

func flatten(prefix string, table map[string]any, out map[string]string) {
	for k, v := range table {
		key := prefix + strings.ToLower(k)
		switch v := v.(type) {
		case map[string]any:
			flatten(key+".", v, out)
		case []any:
			parts := make([]string, len(v))
			for i, e := range v {
				parts[i] = fmt.Sprint(e)
			}
			out[key] = strings.Join(parts, ",")
		default:
			out[key] = fmt.Sprint(v)
		}
	}
}

func applyDefaults(n *notijet.Notifier, names []string) error {
	for _, name := range names {
		if err := n.SetDefault(name); err != nil {
			return err
		}
	}
	return nil
}
