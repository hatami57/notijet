package vonage

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/hatami57/notijet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func server(t *testing.T, status int, reply string) (*Provider, *[]url.Values) {
	t.Helper()
	var calls []url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		assert.Equal(t, "/sms/json", r.URL.Path)
		calls = append(calls, r.PostForm)
		w.WriteHeader(status)
		fmt.Fprint(w, reply)
	}))
	t.Cleanup(srv.Close)
	return New("key", "secret", WithBaseURL(srv.URL), WithSender("Acme"), WithDefaultCountry("44")), &calls
}

func TestSend(t *testing.T) {
	p, calls := server(t, 200, `{"message-count":"2","messages":[
		{"to":"447700900123","message-id":"0A01","status":"0","message-price":"0.0333"},
		{"to":"447700900123","message-id":"0A02","status":"0","message-price":"0.0333"}]}`)
	res, err := p.Send(context.Background(), &notijet.Message{ID: "ref-1", To: []string{"07700 900123"}, Text: "hello"})
	require.NoError(t, err)
	f := (*calls)[0]
	assert.Equal(t, "key", f.Get("api_key"))
	assert.Equal(t, "secret", f.Get("api_secret"))
	assert.Equal(t, "447700900123", f.Get("to"))
	assert.Equal(t, "Acme", f.Get("from"))
	assert.Equal(t, "hello", f.Get("text"))
	assert.Equal(t, "ref-1", f.Get("client-ref"))
	assert.Empty(t, f.Get("type"))
	assert.Equal(t, "0A01", res.MessageID())
	assert.Equal(t, "+447700900123", res.Receipts[0].To)

	_, err = p.Send(context.Background(), &notijet.Message{To: []string{"+447700900123"}, Text: "سلام"})
	require.NoError(t, err)
	assert.Equal(t, "unicode", (*calls)[1].Get("type"))

	long := strings.Repeat("ر", 150)
	_, err = p.Send(context.Background(), &notijet.Message{ID: long, To: []string{"+447700900123"}, Text: "x"})
	require.NoError(t, err)
	assert.Equal(t, strings.Repeat("ر", 100), (*calls)[2].Get("client-ref"), "cut on characters, not bytes")
}

func TestErrors(t *testing.T) {
	cases := []struct {
		status   string
		sentinel error
		retry    bool
	}{
		{"1", notijet.ErrRateLimited, true},
		{"4", notijet.ErrUnauthorized, false},
		{"5", nil, true},
		{"6", notijet.ErrInvalidRecipient, false},
		{"9", notijet.ErrInsufficientCredit, false},
		{"15", notijet.ErrInvalidSender, false},
		{"12", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.status, func(t *testing.T) {
			p, _ := server(t, 200, fmt.Sprintf(`{"message-count":"1","messages":[{"status":"%s","error-text":"err"}]}`, tc.status))
			_, err := p.Send(context.Background(), &notijet.Message{To: []string{"+447700900123"}, Text: "x"})
			require.Error(t, err)
			if tc.sentinel != nil {
				assert.ErrorIs(t, err, tc.sentinel)
			}
			assert.Equal(t, tc.status, notijet.ProviderCode(err))
			assert.Equal(t, tc.retry, notijet.IsRetryable(err))
		})
	}

	p, _ := server(t, 200, `{"message-count":"0","messages":[]}`)
	_, err := p.Send(context.Background(), &notijet.Message{To: []string{"+447700900123"}, Text: "x"})
	assert.True(t, notijet.IsRetryable(err))
	_, err = New("k", "s").Send(context.Background(), &notijet.Message{To: []string{"+447700900123"}, Text: "x"})
	assert.ErrorIs(t, err, notijet.ErrInvalidSender)
}

func TestMoreCases(t *testing.T) {
	var form url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		form = r.PostForm
		fmt.Fprint(w, `{"messages":[{"to":"447700900123","message-id":"x","status":"0"}]}`)
	}))
	defer srv.Close()
	p := New("k", "s", WithBaseURL(srv.URL), WithSender("Acme"), WithCallback("https://x.io/dlr"))
	_, err := p.Send(context.Background(), &notijet.Message{To: []string{"+447700900123"}, Text: "x"})
	require.NoError(t, err)
	assert.Equal(t, "https://x.io/dlr", form.Get("callback"))

	_, err = p.Send(context.Background(), &notijet.Message{To: []string{"07700900123"}, Text: "x"})
	assert.ErrorIs(t, err, notijet.ErrInvalidRecipient, "national form needs WithDefaultCountry")
	_, err = New("", "").Send(context.Background(), &notijet.Message{To: []string{"+447700900123"}, Text: "x"})
	assert.Error(t, err)

	unauthorized, _ := server(t, 401, "Unauthorized")
	_, err = unauthorized.Send(context.Background(), &notijet.Message{To: []string{"+447700900123"}, Text: "x"})
	assert.ErrorIs(t, err, notijet.ErrUnauthorized)
}
