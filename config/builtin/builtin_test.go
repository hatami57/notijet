package builtin

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hatami57/notijet"
	"github.com/hatami57/notijet/config"
	"github.com/hatami57/notijet/webhook"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recorder is a test server that records every request as one string —
// method, path, query, headers (basic auth decoded) and body — and answers
// with reply.
type recorder struct {
	mu    sync.Mutex
	reqs  []string
	reply string
	srv   *httptest.Server
}

func newRecorder(t *testing.T, reply string) *recorder {
	r := &recorder{reply: reply}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		var b strings.Builder
		fmt.Fprintf(&b, "%s %s?%s\n", req.Method, req.URL.EscapedPath(), req.URL.RawQuery)
		for k, v := range req.Header {
			fmt.Fprintf(&b, "%s: %s\n", k, strings.Join(v, ","))
		}
		if u, p, ok := req.BasicAuth(); ok {
			fmt.Fprintf(&b, "basic %s:%s\n", u, p)
		}
		b.Write(body)
		r.mu.Lock()
		r.reqs = append(r.reqs, b.String())
		r.mu.Unlock()
		fmt.Fprint(w, r.reply)
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *recorder) last() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.reqs) == 0 {
		return ""
	}
	return r.reqs[len(r.reqs)-1]
}

func buildOne(t *testing.T, name string, settings map[string]string) (notijet.Provider, error) {
	t.Helper()
	b := &config.Builder{Registry: Registry()}
	built, err := b.Build(context.Background(), []config.Spec{{Name: name, Settings: settings}})
	if err != nil {
		return nil, err
	}
	return built.Providers[0].Provider, nil
}

func TestTypes(t *testing.T) {
	assert.Equal(t, []string{
		"bale", "brevo", "discord", "ghasedak", "infobip", "ippanel", "kavenegar",
		"logger", "mailgun", "melipayamak", "memory", "plivo", "postmark", "resend", "sendgrid",
		"slack", "smsir", "smtp", "telegram", "twilio", "vonage", "webhook",
	}, Registry().Types(), "no type that needs a cloud SDK")
}

var (
	smsMsg   = func() *notijet.Message { return &notijet.Message{To: []string{"09121234567"}, Text: "hi"} }
	intlMsg  = func() *notijet.Message { return &notijet.Message{To: []string{"+447700900123"}, Text: "hi"} }
	mailMsg  = func() *notijet.Message { return &notijet.Message{To: []string{"a@x.io"}, Subject: "s", Text: "t"} }
	hookMsg  = func() *notijet.Message { return &notijet.Message{Text: "hi"} }
	settings = func(kv ...string) map[string]string {
		m := map[string]string{}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return m
	}
)

// TestSettingsReachTheWire builds each HTTP provider from config against a
// recording server and checks the configured credential and sender were
// sent. The reply is generic, so some providers then fail to parse it; only
// the request matters here.
func TestSettingsReachTheWire(t *testing.T) {
	cases := []struct {
		typ  string
		kv   []string
		msg  func() *notijet.Message
		want []string
	}{
		{"kavenegar", []string{"api_key", "cfg-secret", "sender", "5000"}, smsMsg, []string{"/cfg-secret/sms/send.json", "sender=5000"}},
		{"smsir", []string{"api_key", "cfg-secret", "sender", "3000"}, smsMsg, []string{"X-Api-Key: cfg-secret", `"lineNumber":3000`}},
		{"melipayamak", []string{"username", "u", "password", "cfg-secret", "sender", "5000"}, smsMsg, []string{"password=cfg-secret", "from=5000"}},
		{"ghasedak", []string{"api_key", "cfg-secret", "sender", "3000"}, smsMsg, []string{"Apikey: cfg-secret", `"lineNumber":"3000"`}},
		{"ippanel", []string{"api_key", "cfg-secret", "sender", "+983000"}, smsMsg, []string{"Apikey: cfg-secret", `"sender":"+983000"`}},
		{"twilio", []string{"account_sid", "AC1", "auth_token", "cfg-secret", "sender", "+15005550006", "status_callback", "https://cb"}, intlMsg,
			[]string{"basic AC1:cfg-secret", "From=%2B15005550006", "StatusCallback=https"}},
		{"twilio", []string{"account_sid", "AC1", "api_key_sid", "SK1", "api_key_secret", "cfg-secret", "sender", "MG1"}, intlMsg,
			[]string{"basic SK1:cfg-secret", "MessagingServiceSid=MG1"}},
		{"vonage", []string{"api_key", "k", "api_secret", "cfg-secret", "sender", "Acme", "default_country", "44", "callback", "https://cb"}, intlMsg,
			[]string{"api_secret=cfg-secret", "from=Acme", "callback=https"}},
		{"plivo", []string{"auth_id", "MA1", "auth_token", "cfg-secret", "sender", "Acme", "callback", "https://cb"}, intlMsg,
			[]string{"basic MA1:cfg-secret", `"src":"Acme"`, `"url":"https://cb"`}},
		{"infobip", []string{"api_key", "cfg-secret", "sender", "Acme", "notify_url", "https://cb"}, intlMsg,
			[]string{"Authorization: App cfg-secret", `"from":"Acme"`, `"notifyUrl":"https://cb"`}},
		{"sendgrid", []string{"api_key", "cfg-secret", "from", "a@acme.test"}, mailMsg, []string{"Bearer cfg-secret", `"email":"a@acme.test"`}},
		{"mailgun", []string{"domain", "mg.acme.test", "api_key", "cfg-secret", "from", "a@acme.test"}, mailMsg,
			[]string{"/v3/mg.acme.test/messages", "basic api:cfg-secret", "a@acme.test"}},
		{"postmark", []string{"server_token", "cfg-secret", "from", "a@acme.test", "message_stream", "broadcast"}, mailMsg,
			[]string{"X-Postmark-Server-Token: cfg-secret", `"MessageStream":"broadcast"`}},
		{"resend", []string{"api_key", "cfg-secret", "from", "a@acme.test"}, mailMsg, []string{"Bearer cfg-secret", `"from":"a@acme.test"`}},
		{"brevo", []string{"api_key", "cfg-secret", "from", "a@acme.test"}, mailMsg, []string{"Api-Key: cfg-secret", `"email":"a@acme.test"`}},
		{"telegram", []string{"bot_token", "1:cfg-secret", "chat_id", "-100", "silent", "true", "disable_link_preview", "yes"}, hookMsg,
			[]string{"/bot1:cfg-secret/sendMessage", `"chat_id":"-100"`, `"disable_notification":true`, `"link_preview_options"`}},
		{"bale", []string{"bot_token", "1:cfg-secret", "chat_id", "42"}, hookMsg, []string{"/bot1:cfg-secret/sendMessage", `"chat_id":"42"`}},
	}
	for _, tc := range cases {
		t.Run(tc.typ, func(t *testing.T) {
			rec := newRecorder(t, `{}`)
			kv := append([]string{"type", tc.typ, "timeout", "5s", "base_url", rec.srv.URL}, tc.kv...)
			p, err := buildOne(t, "p", settings(kv...))
			require.NoError(t, err)
			_, _ = p.Send(context.Background(), tc.msg())
			got := rec.last()
			require.NotEmpty(t, got, "no request reached the server")
			for _, w := range tc.want {
				assert.Contains(t, got, w)
			}
		})
	}
}

func TestWebhookChatProviders(t *testing.T) {
	rec := newRecorder(t, "ok")
	p, err := buildOne(t, "s", settings("type", "slack", "webhook_url", rec.srv.URL+"/hook/cfg-secret"))
	require.NoError(t, err)
	_, err = p.Send(context.Background(), hookMsg())
	require.NoError(t, err)
	assert.Contains(t, rec.last(), "/hook/cfg-secret")

	rec = newRecorder(t, `{"id":"1"}`)
	p, err = buildOne(t, "d", settings("type", "discord", "webhook_url", rec.srv.URL+"/api/webhooks/1/x",
		"username", "Bot", "avatar_url", "https://a", "mentions", "true"))
	require.NoError(t, err)
	_, err = p.Send(context.Background(), hookMsg())
	require.NoError(t, err)
	assert.Contains(t, rec.last(), `"username":"Bot"`)
	assert.NotContains(t, rec.last(), "allowed_mentions")

	rec = newRecorder(t, `{"id":"evt"}`)
	p, err = buildOne(t, "w", settings("type", "webhook", "url", rec.srv.URL, "secret", "s3", "channel", "CHAT",
		"header.authorization", "Bearer cfg-secret"))
	require.NoError(t, err)
	assert.Equal(t, notijet.ChannelChat, p.Channel())
	_, err = p.Send(context.Background(), hookMsg())
	require.NoError(t, err)
	assert.Contains(t, rec.last(), "Authorization: Bearer cfg-secret")
	assert.Contains(t, rec.last(), webhook.SignatureHeader+": sha256=")
}

func TestTimeoutSetting(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(300 * time.Millisecond)
	}))
	defer slow.Close()
	p, err := buildOne(t, "k", settings("type", "kavenegar", "api_key", "k", "base_url", slow.URL, "timeout", "20ms"))
	require.NoError(t, err)
	start := time.Now()
	_, err = p.Send(context.Background(), smsMsg())
	require.Error(t, err)
	assert.Less(t, time.Since(start), 250*time.Millisecond)
	assert.True(t, notijet.IsRetryable(err))
}

func TestRegionsAndDefaults(t *testing.T) {
	for _, typ := range []string{"sendgrid", "mailgun"} {
		_, err := buildOne(t, "x", settings("type", typ, "api_key", "k", "domain", "d", "region", "eu"))
		if typ == "sendgrid" {
			// sendgrid takes no domain: the typo check catches it.
			assert.ErrorContains(t, err, "unknown setting domain")
			_, err = buildOne(t, "x", settings("type", typ, "api_key", "k", "region", "eu"))
		}
		require.NoError(t, err, typ)
		_, err = buildOne(t, "x", settings("type", typ, "api_key", "k", "domain", "d", "region", "mars"))
		assert.Error(t, err)
	}
}

func TestSMTPFactory(t *testing.T) {
	ok := [][]string{
		{"type", "smtp", "preset", "gmail", "username", "u", "password", "p", "from", "a@x.io", "timeout", "10s"},
		{"type", "smtp", "preset", "yahoo", "port", "587", "security", "starttls"},
		{"type", "smtp", "host", "relay.local", "port", "25", "security", "none", "local_name", "app", "message_id_domain", "x.io"},
		{"type", "smtp", "host", "mail.x.io", "security", "tls"},
	}
	for _, kv := range ok {
		p, err := buildOne(t, "m", settings(kv...))
		require.NoError(t, err, kv)
		assert.Equal(t, "smtp", p.Name())
	}
	bad := map[string][]string{
		"no host":          {"type", "smtp"},
		"preset and host":  {"type", "smtp", "preset", "gmail", "host", "h"},
		"unknown preset":   {"type", "smtp", "preset", "aol"},
		"bad security":     {"type", "smtp", "host", "h", "security", "ssl3"},
		"password no user": {"type", "smtp", "host", "h", "password", "p"},
		"bad port":         {"type", "smtp", "host", "h", "port", "twenty-five"},
	}
	for name, kv := range bad {
		_, err := buildOne(t, "m", settings(kv...))
		assert.Error(t, err, name)
	}
}

func TestRequiredSettings(t *testing.T) {
	cases := map[string]string{
		"kavenegar": "api_key", "smsir": "api_key", "melipayamak": "username", "ghasedak": "api_key",
		"ippanel": "api_key", "twilio": "account_sid", "vonage": "api_secret", "plivo": "auth_token",
		"infobip": "base_url", "sendgrid": "api_key", "mailgun": "domain", "postmark": "server_token",
		"resend": "api_key", "brevo": "api_key", "telegram": "bot_token", "bale": "bot_token",
		"slack": "webhook_url", "discord": "webhook_url", "webhook": "url",
	}
	for typ, key := range cases {
		_, err := buildOne(t, "x", settings("type", typ))
		require.Error(t, err, typ)
		assert.Contains(t, err.Error(), key+" is required", typ)
	}
	_, err := buildOne(t, "x", settings("type", "twilio", "account_sid", "AC"))
	assert.ErrorContains(t, err, "auth_token")
	_, err = buildOne(t, "x", settings("type", "twilio", "account_sid", "AC", "api_key_sid", "SK"))
	assert.ErrorContains(t, err, "go together")
}

func TestDevProviders(t *testing.T) {
	p, err := buildOne(t, "dev-mail", settings("type", "logger", "channel", "email", "level", "debug"))
	require.NoError(t, err)
	assert.Equal(t, "dev-mail", p.Name())
	assert.Equal(t, notijet.ChannelEmail, p.Channel())
	for _, lvl := range []string{"info", "warn", "error"} {
		_, err := buildOne(t, "l", settings("type", "logger", "level", lvl))
		require.NoError(t, err)
	}
	_, err = buildOne(t, "l", settings("type", "logger", "level", "loud"))
	assert.Error(t, err)

	p, err = buildOne(t, "mem", settings("type", "memory", "channel", "push"))
	require.NoError(t, err)
	assert.Equal(t, notijet.ChannelPush, p.Channel())
	p, err = buildOne(t, "mem", settings("type", "memory"))
	require.NoError(t, err)
	assert.Equal(t, notijet.ChannelSMS, p.Channel())
}

// TestNoCloudSDKDependency keeps the package light: the AWS SDK and OAuth2
// belong to config/builtin/aws and config/builtin/google, and a provider
// package importing them would silently pull them back in here.
func TestNoCloudSDKDependency(t *testing.T) {
	if testing.Short() {
		t.Skip("runs go list")
	}
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go tool not found")
	}
	out, err := exec.Command(gobin, "list", "-deps", ".").Output()
	require.NoError(t, err)
	for _, pkg := range strings.Fields(string(out)) {
		for _, banned := range []string{"github.com/aws/", "golang.org/x/oauth2", "cloud.google.com/"} {
			assert.False(t, strings.HasPrefix(pkg, banned), "config/builtin depends on %s", pkg)
		}
	}
}

func TestKavenegarTokenSpaceReplacer(t *testing.T) {
	rec := newRecorder(t, `{"return":{"status":200,"message":"ok"},"entries":[{"messageid":1,"receptor":"09121234567"}]}`)
	p, err := buildOne(t, "otp", settings("type", "kavenegar", "api_key", "k", "base_url", rec.srv.URL, "token_space_replacer", "-"))
	require.NoError(t, err)
	_, err = p.Send(context.Background(), &notijet.Message{
		To:       []string{"09121234567"},
		Template: notijet.NewTemplate("booking", "token", "Ali Rezaei"),
	})
	require.NoError(t, err)
	assert.Contains(t, rec.last(), "token=Ali-Rezaei")
}
