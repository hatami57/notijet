package module

import (
	"context"
	"errors"
	"testing"

	"github.com/hatami57/microjet/core/configx"
	"github.com/hatami57/microjet/core/secretx"
	"github.com/hatami57/microjet/host"
	"github.com/hatami57/notijet"
	"github.com/hatami57/notijet/config/builtin"
	"github.com/hatami57/notijet/memory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func appWithConfig(t *testing.T, values map[string]any, opts ...Option) *host.App {
	t.Helper()
	app, err := host.New(host.WithConfigReader(configx.NewMapReader(values)))
	require.NoError(t, err)
	app.WithModule(New(append([]Option{WithConfig(builtin.Registry())}, opts...)...))
	return app
}

func providersConfig(tables map[string]any) map[string]any {
	return map[string]any{"notijet": map[string]any{"providers": tables}}
}

type secrets map[string]string

func (s secrets) Resolve(_ context.Context, ref string) (secretx.Value, error) {
	v, ok := s[ref]
	if !ok {
		return secretx.Value{}, errors.New("no secret " + ref)
	}
	return secretx.New(v), nil
}

func TestProvidersFromConfig(t *testing.T) {
	coded := memory.New(memory.WithName("coded"))
	app := appWithConfig(t, providersConfig(map[string]any{
		"primary": map[string]any{"type": "memory"},
		"backup":  map[string]any{"type": "memory", "retry_attempts": 2},
		"sms":     map[string]any{"type": "failover", "providers": []any{"primary", "backup"}, "default": true},
		"hook": map[string]any{"type": "webhook", "url": "https://hooks.example/x", "secret": "secret:hook",
			"header": map[string]any{"Authorization": "secret:token"}},
	}), WithProvider("coded", coded), WithSecrets(secrets{"hook": "s3", "token": "Bearer t"}))
	require.NoError(t, app.Err())
	start(t, app)

	n := Notifier(app)
	assert.ElementsMatch(t, []string{"coded", "backup", "hook", "primary", "sms"}, n.Names())
	name, _, err := n.Default(notijet.ChannelSMS)
	require.NoError(t, err)
	assert.Equal(t, "sms", name, "default = true wins over the first-registered rule")
	res, err := n.Send(context.Background(), notijet.ChannelSMS, &notijet.Message{To: []string{"1"}, Text: "x"})
	require.NoError(t, err)
	assert.Equal(t, "primary", res.Provider)
}

func TestCodeDefaultMayNameConfiguredProvider(t *testing.T) {
	app := appWithConfig(t, providersConfig(map[string]any{
		"cfg": map[string]any{"type": "memory"},
	}), WithProvider("coded", memory.New()), WithDefault("cfg"))
	start(t, app)
	name, _, err := Notifier(app).Default(notijet.ChannelSMS)
	require.NoError(t, err)
	assert.Equal(t, "cfg", name)
}

func TestConfigErrorsStopStartup(t *testing.T) {
	cases := map[string]map[string]any{
		"unknown type":   providersConfig(map[string]any{"x": map[string]any{"type": "pigeon"}}),
		"typo":           providersConfig(map[string]any{"x": map[string]any{"type": "memory", "chanel": "sms"}}),
		"not a table":    providersConfig(map[string]any{"x": "memory"}),
		"missing secret": providersConfig(map[string]any{"x": map[string]any{"type": "webhook", "url": "secret:nope"}}),
		"dup name":       providersConfig(map[string]any{"coded": map[string]any{"type": "memory"}}),
	}
	for name, values := range cases {
		t.Run(name, func(t *testing.T) {
			app := appWithConfig(t, values, WithProvider("coded", memory.New()), WithSecrets(secrets{}))
			assert.Error(t, app.Start(context.Background()))
		})
	}
	bad := appWithConfig(t, providersConfig(map[string]any{"x": map[string]any{"type": "memory"}}), WithDefault("nope"))
	assert.ErrorIs(t, bad.Start(context.Background()), notijet.ErrNoProvider)
}

func TestNoConfigSection(t *testing.T) {
	app := appWithConfig(t, map[string]any{}, WithProvider("", memory.New()))
	start(t, app)
	assert.Equal(t, []string{"memory"}, Notifier(app).Names())
}

func TestSpecs(t *testing.T) {
	specs := Specs(map[string]any{
		"b": map[string]any{"Type": "x", "Port": 25, "On": true, "List": []any{"a", 1}, "Nested": map[string]any{"Deep": map[string]any{"K": "v"}}},
		"a": "scalar",
	})
	require.Len(t, specs, 2)
	assert.Equal(t, "a", specs[0].Name, "sorted by name")
	assert.Equal(t, map[string]string{"type": ""}, specs[0].Settings)
	assert.Equal(t, map[string]string{"type": "x", "port": "25", "on": "true", "list": "a,1", "nested.deep.k": "v"}, specs[1].Settings)
}
