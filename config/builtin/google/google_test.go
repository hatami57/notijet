package google

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/hatami57/notijet"
	"github.com/hatami57/notijet/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func buildOne(t *testing.T, name string, kv ...string) (notijet.Provider, error) {
	t.Helper()
	settings := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		settings[kv[i]] = kv[i+1]
	}
	b := &config.Builder{Registry: Register(config.NewRegistry())}
	built, err := b.Build(context.Background(), []config.Spec{{Name: name, Settings: settings}})
	if err != nil {
		return nil, err
	}
	return built.Providers[0].Provider, nil
}

// recorder is a test server that records each request's path and headers
// and answers with reply.
type recorder struct {
	mu    sync.Mutex
	reqs  []string
	reply string
	srv   *httptest.Server
}

func newRecorder(t *testing.T, reply string) *recorder {
	r := &recorder{reply: reply}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var b strings.Builder
		fmt.Fprintf(&b, "%s %s\n", req.Method, req.URL.EscapedPath())
		for k, v := range req.Header {
			fmt.Fprintf(&b, "%s: %s\n", k, strings.Join(v, ","))
		}
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

// serviceAccount returns a service account key whose token_uri is a fake
// OAuth server issuing "test-token".
func serviceAccount(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	oauth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		assert.Equal(t, "urn:ietf:params:oauth:grant-type:jwt-bearer", r.PostForm.Get("grant_type"))
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":"test-token","token_type":"Bearer","expires_in":3600}`)
	}))
	t.Cleanup(oauth.Close)
	sa, err := json.Marshal(map[string]string{
		"type":           "service_account",
		"project_id":     "p",
		"private_key_id": "k1",
		"private_key":    string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		"client_email":   "notijet@p.iam.gserviceaccount.com",
		"client_id":      "1",
		"token_uri":      oauth.URL,
	})
	require.NoError(t, err)
	return sa
}

func TestGoogleProviders(t *testing.T) {
	sa := serviceAccount(t)
	file := filepath.Join(t.TempDir(), "sa.json")
	require.NoError(t, os.WriteFile(file, sa, 0o600))

	rec := newRecorder(t, `{"id":"g1","name":"projects/p/messages/1"}`)
	p, err := buildOne(t, "g", "type", "gmail", "credentials_json", string(sa), "user", "ali@acme.test",
		"from", "ali@acme.test", "base_url", rec.srv.URL, "timeout", "5s")
	require.NoError(t, err)
	res, err := p.Send(context.Background(), &notijet.Message{To: []string{"a@x.io"}, Subject: "s", Text: "t"})
	require.NoError(t, err)
	assert.Equal(t, "g1", res.MessageID())
	assert.Contains(t, rec.last(), "Authorization: Bearer test-token")
	assert.Contains(t, rec.last(), "/users/ali@acme.test/messages/send")

	p, err = buildOne(t, "f", "type", "fcm", "project_id", "p", "credentials_file", file, "base_url", rec.srv.URL)
	require.NoError(t, err)
	_, err = p.Send(context.Background(), &notijet.Message{To: []string{"tok"}, Text: "hi"})
	require.NoError(t, err)
	assert.Contains(t, rec.last(), "/v1/projects/p/messages:send")
	assert.Contains(t, rec.last(), "Authorization: Bearer test-token")

	// Application Default Credentials.
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", file)
	p, err = buildOne(t, "f", "type", "fcm", "project_id", "p", "base_url", rec.srv.URL)
	require.NoError(t, err)
	_, err = p.Send(context.Background(), &notijet.Message{To: []string{"tok"}, Text: "hi"})
	require.NoError(t, err)

	bad := map[string][]string{
		"both":         {"type", "fcm", "project_id", "p", "credentials_json", string(sa), "credentials_file", file},
		"missing file": {"type", "fcm", "project_id", "p", "credentials_file", "/nonexistent.json"},
		"bad json":     {"type", "fcm", "project_id", "p", "credentials_json", "{"},
		"user no key":  {"type", "gmail", "user", "ali@acme.test"},
	}
	for name, kv := range bad {
		_, err := buildOne(t, "x", kv...)
		assert.Error(t, err, name)
	}
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "/nonexistent.json")
	_, err = buildOne(t, "x", "type", "fcm", "project_id", "p")
	assert.ErrorContains(t, err, "no credentials")
}

func TestFCMRequiresProject(t *testing.T) {
	_, err := buildOne(t, "x", "type", "fcm")
	assert.ErrorContains(t, err, "project_id is required")
}
