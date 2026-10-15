package fcm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hatami57/notijet"
	"github.com/hatami57/notijet/providertest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type call struct {
	path, auth string
	message    map[string]any
}

// server answers each request with reply(message).
func server(t *testing.T, reply func(m map[string]any) (int, string), opts ...Option) (*Provider, *[]call) {
	t.Helper()
	var calls []call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body struct {
			Message map[string]any `json:"message"`
		}
		require.NoError(t, json.Unmarshal(b, &body))
		calls = append(calls, call{r.URL.Path, r.Header.Get("Authorization"), body.Message})
		status, out := reply(body.Message)
		w.WriteHeader(status)
		fmt.Fprint(w, out)
	}))
	t.Cleanup(srv.Close)
	base := []Option{WithBaseURL(srv.URL), WithToken(func(context.Context) (string, error) { return "ya29.tok", nil })}
	return New("my-project", append(base, opts...)...), &calls
}

func okReply(map[string]any) (int, string) {
	return 200, `{"name":"projects/my-project/messages/0:1"}`
}

func fcmError(status int, gstatus, code, msg string) string {
	return fmt.Sprintf(`{"error":{"code":%d,"message":%q,"status":%q,"details":[{"@type":"type.googleapis.com/google.firebase.fcm.v1.FcmError","errorCode":%q}]}}`,
		status, msg, gstatus, code)
}

func TestSend(t *testing.T) {
	p, calls := server(t, okReply)
	res, err := p.Send(context.Background(), &notijet.Message{
		To:      []string{"tok-1", "topic:news", "/topics/sport", "condition:'a' in topics"},
		Subject: "Order shipped",
		Text:    "Arriving tomorrow",
		Data:    map[string]string{"order": "42"},
	})
	require.NoError(t, err)
	require.Len(t, *calls, 4)
	c := (*calls)[0]
	assert.Equal(t, "/v1/projects/my-project/messages:send", c.path)
	assert.Equal(t, "Bearer ya29.tok", c.auth)
	assert.Equal(t, map[string]any{
		"token":        "tok-1",
		"notification": map[string]any{"title": "Order shipped", "body": "Arriving tomorrow"},
		"data":         map[string]any{"order": "42"},
	}, c.message)
	assert.Equal(t, "news", (*calls)[1].message["topic"])
	assert.Equal(t, "sport", (*calls)[2].message["topic"])
	assert.Equal(t, "'a' in topics", (*calls)[3].message["condition"])
	assert.Equal(t, "projects/my-project/messages/0:1", res.MessageID())
	assert.Len(t, res.Receipts, 4)
}

func TestDataOnly(t *testing.T) {
	p, calls := server(t, okReply)
	_, err := p.Send(context.Background(), &notijet.Message{To: []string{"tok"}, Data: map[string]string{"sync": "1"}})
	require.NoError(t, err)
	assert.Nil(t, (*calls)[0].message["notification"])
}

func TestStaleTokensDoNotFailTheSend(t *testing.T) {
	p, _ := server(t, func(m map[string]any) (int, string) {
		switch m["token"] {
		case "stale":
			return 404, fcmError(404, "NOT_FOUND", "UNREGISTERED", "Requested entity was not found.")
		case "garbage":
			return 400, fcmError(400, "INVALID_ARGUMENT", "INVALID_ARGUMENT", "The registration token is not a valid FCM registration token")
		}
		return okReply(m)
	})
	res, err := p.Send(context.Background(), &notijet.Message{To: []string{"stale", "good", "garbage"}, Text: "x"})
	require.NoError(t, err)
	require.Len(t, res.Receipts, 3)
	assert.Equal(t, "invalid: UNREGISTERED", res.Receipts[0].Status)
	assert.Empty(t, res.Receipts[0].MessageID)
	assert.Equal(t, "sent", res.Receipts[1].Status)
	assert.Equal(t, "invalid: INVALID_ARGUMENT", res.Receipts[2].Status)

	_, err = p.Send(context.Background(), &notijet.Message{To: []string{"stale"}, Text: "x"})
	assert.ErrorIs(t, err, notijet.ErrInvalidRecipient, "all invalid fails the send")
	assert.Equal(t, "UNREGISTERED", notijet.ProviderCode(err))
}

func TestFailureAfterSuccessIsPartial(t *testing.T) {
	n := 0
	p, _ := server(t, func(m map[string]any) (int, string) {
		n++
		if n == 2 {
			return 403, fcmError(403, "PERMISSION_DENIED", "SENDER_ID_MISMATCH", "mismatch")
		}
		return okReply(m)
	})
	_, err := p.Send(context.Background(), &notijet.Message{To: []string{"a", "b"}, Text: "x"})
	var pe *notijet.PartialError
	require.ErrorAs(t, err, &pe)
	assert.Len(t, pe.Result.Receipts, 1)
	assert.ErrorIs(t, err, notijet.ErrUnauthorized)

	n = 1
	_, err = p.Send(context.Background(), &notijet.Message{To: []string{"a"}, Text: "x"})
	assert.False(t, errors.As(err, &pe), "a failure before any success is returned as is")
}

func TestErrors(t *testing.T) {
	cases := []struct {
		status   int
		body     string
		code     string
		sentinel error
		retry    bool
	}{
		{403, fcmError(403, "PERMISSION_DENIED", "SENDER_ID_MISMATCH", "x"), "SENDER_ID_MISMATCH", notijet.ErrUnauthorized, false},
		{401, fcmError(401, "UNAUTHENTICATED", "THIRD_PARTY_AUTH_ERROR", "x"), "THIRD_PARTY_AUTH_ERROR", notijet.ErrUnauthorized, false},
		{400, fcmError(400, "INVALID_ARGUMENT", "INVALID_ARGUMENT", "Invalid JSON payload"), "INVALID_ARGUMENT", nil, false},
		{400, `{"error":{"code":400,"message":"quota","status":"RESOURCE_EXHAUSTED","details":[{"errorCode":"QUOTA_EXCEEDED"}]}}`, "QUOTA_EXCEEDED", notijet.ErrRateLimited, true},
		{401, `{"error":{"code":401,"message":"Request had invalid authentication credentials.","status":"UNAUTHENTICATED"}}`, "UNAUTHENTICATED", notijet.ErrUnauthorized, false},
		{400, `bad`, "400", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			p, _ := server(t, func(map[string]any) (int, string) { return tc.status, tc.body })
			_, err := p.Send(context.Background(), &notijet.Message{To: []string{"tok"}, Text: "x"})
			require.Error(t, err)
			if tc.sentinel != nil {
				assert.ErrorIs(t, err, tc.sentinel)
			}
			assert.Equal(t, tc.code, notijet.ProviderCode(err))
			assert.Equal(t, tc.retry, notijet.IsRetryable(err))
		})
	}

	p, _ := server(t, func(map[string]any) (int, string) { return 200, `{}` })
	_, err := p.Send(context.Background(), &notijet.Message{To: []string{"tok"}, Text: "x"})
	assert.True(t, notijet.IsRetryable(err), "a reply without name is unexpected")
}

func TestValidation(t *testing.T) {
	p, calls := server(t, okReply)
	cases := map[string]*notijet.Message{
		"nil":          nil,
		"no recipient": {Text: "x"},
		"attachments":  {To: []string{"t"}, Text: "x", Attachments: []notijet.Attachment{{Filename: "a"}}},
		"cc":           {To: []string{"t"}, Text: "x", CC: []string{"c"}},
		"html only":    {To: []string{"t"}, HTML: "<b>x</b>"},
		"template":     {To: []string{"t"}, Template: notijet.NewTemplate("t")},
		"empty":        {To: []string{"t"}},
	}
	for name, msg := range cases {
		_, err := p.Send(context.Background(), msg)
		assert.Error(t, err, name)
		assert.False(t, notijet.IsRetryable(err), name)
	}
	_, err := New("").Send(context.Background(), &notijet.Message{To: []string{"t"}, Text: "x"})
	assert.ErrorContains(t, err, "project")
	_, err = New("p").Send(context.Background(), &notijet.Message{To: []string{"t"}, Text: "x"})
	assert.ErrorContains(t, err, "credentials")
	failing, _ := server(t, okReply, WithToken(func(context.Context) (string, error) { return "", errors.New("expired") }))
	_, err = failing.Send(context.Background(), &notijet.Message{To: []string{"t"}, Text: "x"})
	assert.ErrorContains(t, err, "OAuth2 token")
	assert.Empty(t, *calls)
	assert.Equal(t, notijet.ChannelPush, p.Channel())
}

func TestCancelBetweenRecipients(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	// Cancel once the first recipient has been sent, as the second starts.
	p, _ := server(t, okReply, WithToken(func(context.Context) (string, error) {
		if calls++; calls == 2 {
			cancel()
		}
		return "tok", nil
	}))
	_, err := p.Send(ctx, &notijet.Message{To: []string{"a", "b"}, Text: "x"})
	var pe *notijet.PartialError
	require.ErrorAs(t, err, &pe)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestConformance(t *testing.T) {
	providertest.RunHTTP(t, providertest.HTTPCase{
		Channel: notijet.ChannelPush,
		New: func(baseURL string, hc *http.Client) notijet.Provider {
			return New("my-project", WithBaseURL(baseURL), WithHTTPClient(hc),
				WithToken(func(context.Context) (string, error) { return "s3cr3t-token", nil }))
		},
		Message: func() *notijet.Message { return &notijet.Message{To: []string{"tok"}, Text: "hi"} },
		Secret:  "s3cr3t-token",
	})
}
