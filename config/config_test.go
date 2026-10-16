package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hatami57/notijet"
	"github.com/hatami57/notijet/memory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testRegistry has a "mem" type with a required and an optional setting.
func testRegistry() *Registry {
	return NewRegistry().Register("MEM", func(_ context.Context, s *Settings) (notijet.Provider, error) {
		name := s.Required("label")
		ch := s.OneOf("channel", "sms", "email")
		if ch == "" {
			ch = "sms"
		}
		return memory.New(memory.WithName(name), memory.WithChannel(notijet.Channel(ch))), nil
	})
}

func build(t *testing.T, specs ...Spec) (*Built, error) {
	t.Helper()
	return (&Builder{Registry: testRegistry()}).Build(context.Background(), specs)
}

func TestBuildAndRegister(t *testing.T) {
	b, err := build(t,
		Spec{"a", map[string]string{"type": "mem", "label": "A"}},
		Spec{"mail", map[string]string{"Type": "Mem", "label": "M", "channel": "EMAIL"}},
		Spec{"b", map[string]string{"type": "mem", "label": "B", "default": "true"}},
	)
	require.NoError(t, err)
	require.Len(t, b.Providers, 3)
	assert.Equal(t, []string{"b"}, b.Defaults)

	n := notijet.NewNotifier(notijet.WithLogger(nil))
	require.NoError(t, b.Register(n))
	name, p, err := n.Default(notijet.ChannelSMS)
	require.NoError(t, err)
	assert.Equal(t, "b", name)
	assert.Equal(t, "B", p.Name())
	_, p, _ = n.Default(notijet.ChannelEmail)
	assert.Equal(t, "M", p.Name())
	assert.Error(t, b.Register(n), "registering twice")
	assert.Equal(t, []string{"mem"}, testRegistry().Types())
}

func TestFailoverAndRetry(t *testing.T) {
	b, err := build(t,
		Spec{"sms", map[string]string{"type": "failover", "providers": "primary, backup", "retry_attempts": "2", "retry_initial": "1ms"}},
		Spec{"primary", map[string]string{"type": "mem", "label": "P"}},
		Spec{"backup", map[string]string{"type": "mem", "label": "B", "retry_attempts": "3"}},
		Spec{"outer", map[string]string{"type": "failover", "providers": "sms,backup"}},
	)
	require.NoError(t, err)
	byName := map[string]notijet.Provider{}
	for _, p := range b.Providers {
		byName[p.Name] = p.Provider
	}
	assert.Equal(t, "failover", byName["sms"].Name(), "Retry keeps the inner name")
	assert.Equal(t, "B", byName["backup"].Name())

	// The failover reaches the second member when the first fails.
	primary := unwrapMemory(t, b, "primary")
	primary.FailWith(func(*notijet.Message) error { return notijet.Rejected("P", "", "no", nil) })
	res, err := byName["sms"].Send(context.Background(), &notijet.Message{To: []string{"1"}, Text: "x"})
	require.NoError(t, err)
	assert.Equal(t, "B", res.Provider)
	_, err = byName["outer"].Send(context.Background(), &notijet.Message{To: []string{"1"}, Text: "x"})
	require.NoError(t, err)
}

func unwrapMemory(t *testing.T, b *Built, name string) *memory.Provider {
	t.Helper()
	for _, p := range b.Providers {
		if m, ok := p.Provider.(*memory.Provider); ok && p.Name == name {
			return m
		}
	}
	t.Fatalf("no memory provider %s", name)
	return nil
}

func TestBuildErrors(t *testing.T) {
	cases := map[string][]Spec{
		"no name":          {{"", map[string]string{"type": "mem"}}},
		"duplicate":        {{"a", map[string]string{"type": "mem", "label": "x"}}, {"a", map[string]string{"type": "mem", "label": "x"}}},
		"no type":          {{"a", map[string]string{"label": "x"}}},
		"unknown type":     {{"a", map[string]string{"type": "pigeon"}}},
		"missing required": {{"a", map[string]string{"type": "mem"}}},
		"unknown key":      {{"a", map[string]string{"type": "mem", "label": "x", "lable": "typo"}}},
		"bad enum":         {{"a", map[string]string{"type": "mem", "label": "x", "channel": "fax"}}},
		"bad retry":        {{"a", map[string]string{"type": "mem", "label": "x", "retry_attempts": "many"}}},
		"bad duration":     {{"a", map[string]string{"type": "mem", "label": "x", "retry_max": "soon"}}},
		"empty failover":   {{"a", map[string]string{"type": "failover"}}},
		"unknown member":   {{"a", map[string]string{"type": "failover", "providers": "ghost"}}},
		"cycle": {
			{"a", map[string]string{"type": "failover", "providers": "b"}},
			{"b", map[string]string{"type": "failover", "providers": "a"}},
		},
		"failover typo": {{"a", map[string]string{"type": "failover", "providers": "b", "provider": "c"}}, {"b", map[string]string{"type": "mem", "label": "x"}}},
	}
	for name, specs := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := build(t, specs...)
			require.Error(t, err)
			assert.False(t, notijet.IsRetryable(err))
		})
	}

	_, err := build(t, Spec{"a", map[string]string{"type": "mem"}}, Spec{"f", map[string]string{"type": "failover", "providers": "a"}})
	require.Error(t, err)
	assert.Equal(t, 1, strings.Count(err.Error(), "label is required"), "a member's error is reported once")
	_, err = build(t, Spec{"a", map[string]string{"type": "unknownx"}})
	assert.ErrorContains(t, err, "known: mem")
}

func TestSecretReferences(t *testing.T) {
	t.Setenv("NOTIJET_TEST_LABEL", "from-env")
	path := filepath.Join(t.TempDir(), "label")
	require.NoError(t, os.WriteFile(path, []byte("from-file\n"), 0o600))

	resolved := map[string]string{}
	bld := &Builder{
		Registry: NewRegistry().Register("rec", func(_ context.Context, s *Settings) (notijet.Provider, error) {
			for _, k := range []string{"a", "b", "c", "d"} {
				resolved[k] = s.String(k)
			}
			return memory.New(), nil
		}),
		Resolve: func(_ context.Context, ref string) (string, error) { return "resolved(" + ref + ")", nil },
		Schemes: []string{"secret"},
	}
	_, err := bld.Build(context.Background(), []Spec{{"x", map[string]string{
		"type": "rec", "a": "env:NOTIJET_TEST_LABEL", "b": "file:" + path, "c": "secret:prod/sms", "d": "https://literal:colon",
	}}})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		"a": "from-env", "b": "from-file", "c": "resolved(secret:prod/sms)", "d": "https://literal:colon",
	}, resolved)

	_, err = bld.Build(context.Background(), []Spec{{"x", map[string]string{"type": "rec", "a": "env:NOTIJET_TEST_UNSET_VAR"}}})
	assert.ErrorContains(t, err, "NOTIJET_TEST_UNSET_VAR is not set")
	_, err = bld.Build(context.Background(), []Spec{{"x", map[string]string{"type": "rec", "a": "file:/nonexistent/x"}}})
	assert.Error(t, err)

	failing := &Builder{Registry: bld.Registry, Schemes: []string{"secret"},
		Resolve: func(context.Context, string) (string, error) { return "", errors.New("vault down") }}
	_, err = failing.Build(context.Background(), []Spec{{"x", map[string]string{"type": "rec", "a": "secret:x"}}})
	assert.ErrorContains(t, err, "vault down")
}

func TestSettings(t *testing.T) {
	s := NewSettings("n", map[string]string{
		"on": "yes", "off": "0", "bad": "maybe", "n": " 42 ", "nan": "x", "d": "90s", "neg": "-1s", "channel": "sms",
		"list": " a, ,b ", "header.x-a": "1", "header.x-b": "2", "blank": "  ",
	})
	assert.Equal(t, "n", s.Name())
	assert.True(t, s.Bool("on"))
	assert.False(t, s.Bool("off"))
	assert.False(t, s.Bool("missing"))
	assert.False(t, s.Bool("bad"))
	assert.Equal(t, 42, s.Int("n"))
	assert.Equal(t, 0, s.Int("missing"))
	s.Int("nan")
	assert.Equal(t, 90*time.Second, s.Duration("d"))
	assert.Zero(t, s.Duration("missing"))
	s.Duration("neg")
	assert.Equal(t, []string{"a", "b"}, s.List("list"))
	assert.Equal(t, map[string]string{"x-a": "1", "x-b": "2"}, s.Prefixed("header."))
	assert.Empty(t, s.Required("blank"), "blank counts as missing")
	assert.Empty(t, s.OneOf("missing", "x"))
	assert.Equal(t, "sms", s.OneOf("channel", "SMS", "Email"))
	s.Errorf("custom %d", 1)

	err := s.Err()
	require.Error(t, err)
	for _, want := range []string{`bad: "maybe" is not a boolean`, `nan: "x" is not an integer`, `neg: "-1s" is not a duration`, "blank is required", "custom 1"} {
		assert.ErrorContains(t, err, want)
	}

	unused := NewSettings("n", map[string]string{"type": "x", "default": "true", "extra": "1"})
	assert.EqualError(t, unused.Err(), "unknown setting extra")
}
