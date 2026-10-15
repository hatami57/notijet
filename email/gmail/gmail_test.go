package gmail

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hatami57/notijet"
	"github.com/hatami57/notijet/providertest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type call struct {
	path, query, auth, contentType, body string
}

func server(t *testing.T, status int, reply string, opts ...Option) (*Provider, *[]call) {
	t.Helper()
	var calls []call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		calls = append(calls, call{r.URL.EscapedPath(), r.URL.RawQuery, r.Header.Get("Authorization"), r.Header.Get("Content-Type"), string(b)})
		w.WriteHeader(status)
		fmt.Fprint(w, reply)
	}))
	t.Cleanup(srv.Close)
	base := []Option{WithBaseURL(srv.URL), WithFrom("Me <me@acme.test>"),
		WithToken(func(context.Context) (string, error) { return "ya29.tok", nil })}
	return New(append(base, opts...)...), &calls
}

const ok = `{"id":"18f2a","threadId":"18f2a","labelIds":["SENT"]}`

func TestSend(t *testing.T) {
	p, calls := server(t, 200, ok)
	res, err := p.Send(context.Background(), &notijet.Message{
		To:      []string{"a@x.io"},
		BCC:     []string{"hidden@x.io"},
		Subject: "Hello",
		Text:    "plain",
	})
	require.NoError(t, err)
	c := (*calls)[0]
	assert.Equal(t, "/upload/gmail/v1/users/me/messages/send", c.path)
	assert.Equal(t, "uploadType=media", c.query)
	assert.Equal(t, "Bearer ya29.tok", c.auth)
	assert.Equal(t, "message/rfc822", c.contentType)
	assert.Contains(t, c.body, "From: \"Me\" <me@acme.test>\r\n")
	assert.Contains(t, c.body, "Bcc: <hidden@x.io>\r\n", "Gmail learns BCC recipients from the header")
	assert.Contains(t, c.body, "Subject: Hello\r\n")

	assert.Equal(t, "gmail", res.Provider)
	assert.Equal(t, "18f2a", res.MessageID())
	assert.Equal(t, []string{"a@x.io", "hidden@x.io"}, []string{res.Receipts[0].To, res.Receipts[1].To})
}

func TestUserAndClientAuth(t *testing.T) {
	var path, auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, auth = r.URL.EscapedPath(), r.Header.Get("Authorization")
		fmt.Fprint(w, ok)
	}))
	defer srv.Close()
	p := New(WithBaseURL(srv.URL), WithHTTPClient(srv.Client()), WithUser("ali@acme.test"))
	_, err := p.Send(context.Background(), &notijet.Message{From: "ali@acme.test", To: []string{"a@x.io"}, Subject: "s", Text: "t"})
	require.NoError(t, err)
	assert.Equal(t, "/upload/gmail/v1/users/ali@acme.test/messages/send", path)
	assert.Empty(t, auth, "an OAuth2 client adds its own header")
}

func TestErrors(t *testing.T) {
	gerr := func(code int, reason, msg string) string {
		return fmt.Sprintf(`{"error":{"code":%d,"message":%q,"errors":[{"message":%q,"domain":"global","reason":%q}],"status":"X"}}`, code, msg, msg, reason)
	}
	cases := []struct {
		status   int
		body     string
		code     string
		sentinel error
		retry    bool
	}{
		{401, gerr(401, "authError", "Invalid Credentials"), "authError", notijet.ErrUnauthorized, false},
		{403, gerr(403, "insufficientPermissions", "Insufficient Permission"), "insufficientPermissions", notijet.ErrUnauthorized, false},
		{403, gerr(403, "userRateLimitExceeded", "User-rate limit exceeded"), "userRateLimitExceeded", notijet.ErrRateLimited, true},
		{403, gerr(403, "dailyLimitExceeded", "Daily Limit Exceeded"), "dailyLimitExceeded", nil, false},
		{400, gerr(400, "invalidArgument", "Invalid To header"), "invalidArgument", notijet.ErrInvalidRecipient, false},
		{400, gerr(400, "invalidArgument", "Invalid From header"), "invalidArgument", notijet.ErrInvalidSender, false},
		{400, gerr(400, "failedPrecondition", "Precondition check failed."), "failedPrecondition", nil, false},
		{404, `Not Found`, "404", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			p, _ := server(t, tc.status, tc.body)
			_, err := p.Send(context.Background(), &notijet.Message{To: []string{"a@x.io"}, Subject: "s", Text: "t"})
			require.Error(t, err)
			if tc.sentinel != nil {
				assert.ErrorIs(t, err, tc.sentinel)
			}
			assert.Equal(t, tc.code, notijet.ProviderCode(err))
			assert.Equal(t, tc.retry, notijet.IsRetryable(err))
		})
	}

	p, _ := server(t, 200, `{"threadId":"x"}`)
	_, err := p.Send(context.Background(), &notijet.Message{To: []string{"a@x.io"}, Subject: "s", Text: "t"})
	assert.True(t, notijet.IsRetryable(err), "a reply without id is unexpected")
}

func TestValidationAndToken(t *testing.T) {
	p, calls := server(t, 200, ok)
	_, err := p.Send(context.Background(), &notijet.Message{To: []string{"a@x.io"}, Subject: "s", Template: notijet.NewTemplate("t")})
	assert.ErrorIs(t, err, notijet.ErrUnsupported)
	_, err = p.Send(context.Background(), &notijet.Message{To: []string{"bad"}, Subject: "s", Text: "t"})
	assert.ErrorIs(t, err, notijet.ErrInvalidRecipient)
	_, err = New().Send(context.Background(), &notijet.Message{From: "a@x.io", To: []string{"a@x.io"}, Subject: "s", Text: "t"})
	assert.ErrorContains(t, err, "credentials")

	failing, _ := server(t, 200, ok, WithToken(func(context.Context) (string, error) { return "", errors.New("refresh failed") }))
	_, err = failing.Send(context.Background(), &notijet.Message{To: []string{"a@x.io"}, Subject: "s", Text: "t"})
	assert.ErrorContains(t, err, "OAuth2 token")
	assert.Empty(t, *calls)
	assert.Equal(t, "gmail", p.Name())
	assert.True(t, strings.HasPrefix(DefaultBaseURL, "https://"))
}

func TestConformance(t *testing.T) {
	providertest.RunHTTP(t, providertest.HTTPCase{
		Channel: notijet.ChannelEmail,
		New: func(baseURL string, hc *http.Client) notijet.Provider {
			return New(WithBaseURL(baseURL), WithHTTPClient(hc), WithFrom("a@acme.test"),
				WithToken(func(context.Context) (string, error) { return "s3cr3t-token", nil }))
		},
		Message: func() *notijet.Message { return &notijet.Message{To: []string{"b@x.io"}, Subject: "s", Text: "t"} },
		Secret:  "s3cr3t-token",
	})
}
