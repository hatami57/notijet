package resend

import (
	"context"
	"encoding/json"
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
	path, auth, idem string
	body             map[string]any
}

func server(t *testing.T, status int, reply string) (*Provider, *[]call) {
	t.Helper()
	var calls []call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		require.NoError(t, json.Unmarshal(b, &body))
		calls = append(calls, call{r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Idempotency-Key"), body})
		w.WriteHeader(status)
		fmt.Fprint(w, reply)
	}))
	t.Cleanup(srv.Close)
	return New("re_key", WithBaseURL(srv.URL), WithFrom("Acme <no-reply@acme.test>")), &calls
}

const ok = `{"id":"49a3999c-0ce1-4ea6-ab68-afcd6dc2e794"}`

func TestSend(t *testing.T) {
	p, calls := server(t, 200, ok)
	res, err := p.Send(context.Background(), &notijet.Message{
		ID:          "order-42-welcome",
		To:          []string{"Ali <a@x.io>"},
		CC:          []string{"c@x.io"},
		BCC:         []string{"b@x.io"},
		ReplyTo:     []string{"r@x.io"},
		Subject:     "Hello",
		Text:        "plain",
		HTML:        `<img src="cid:logo">`,
		Headers:     map[string]string{"X-Campaign": "fall"},
		Metadata:    map[string]string{"order": "42", "kind": "welcome"},
		Attachments: []notijet.Attachment{{Filename: "a.pdf", Content: []byte("PDF")}, {ContentID: "logo", ContentType: "image/png", Content: []byte("PNG")}},
	})
	require.NoError(t, err)
	assert.Equal(t, "49a3999c-0ce1-4ea6-ab68-afcd6dc2e794", res.MessageID())

	c := (*calls)[0]
	assert.Equal(t, "/emails", c.path)
	assert.Equal(t, "Bearer re_key", c.auth)
	assert.Equal(t, "order-42-welcome", c.idem)
	b := c.body
	assert.Equal(t, "Acme <no-reply@acme.test>", b["from"])
	assert.Equal(t, []any{"Ali <a@x.io>"}, b["to"])
	assert.Equal(t, []any{"c@x.io"}, b["cc"])
	assert.Equal(t, []any{"b@x.io"}, b["bcc"])
	assert.Equal(t, []any{"r@x.io"}, b["reply_to"])
	assert.Equal(t, "Hello", b["subject"])
	assert.Equal(t, "plain", b["text"])
	assert.Equal(t, map[string]any{"X-Campaign": "fall"}, b["headers"])
	assert.Equal(t, []any{map[string]any{"name": "kind", "value": "welcome"}, map[string]any{"name": "order", "value": "42"}}, b["tags"])
	assert.Equal(t, []any{
		map[string]any{"filename": "a.pdf", "content": "UERG", "content_type": "application/pdf"},
		map[string]any{"filename": "logo", "content": "UE5H", "content_type": "image/png", "content_id": "logo"},
	}, b["attachments"])
}

func TestTemplate(t *testing.T) {
	p, calls := server(t, 200, ok)
	_, err := p.Send(context.Background(), &notijet.Message{
		To: []string{"a@x.io"}, Template: notijet.NewTemplate("welcome", "NAME", "Ali"), HTML: "fallback",
	})
	require.NoError(t, err)
	b := (*calls)[0].body
	assert.Equal(t, map[string]any{"id": "welcome", "variables": map[string]any{"NAME": "Ali"}}, b["template"])
	assert.Nil(t, b["html"], "a template cannot be combined with html")
	assert.Empty(t, (*calls)[0].idem, "no Message.ID, no idempotency key")
}

func TestErrors(t *testing.T) {
	cases := []struct {
		status   int
		body     string
		code     string
		sentinel error
		retry    bool
	}{
		{401, `{"statusCode":401,"message":"Missing API key","name":"missing_api_key"}`, "missing_api_key", notijet.ErrUnauthorized, false},
		{403, `{"statusCode":403,"message":"API key is suspended","name":"suspended_api_key"}`, "suspended_api_key", notijet.ErrUnauthorized, false},
		{429, `{"statusCode":429,"message":"Too many requests","name":"rate_limit_exceeded"}`, "rate_limit_exceeded", notijet.ErrRateLimited, true},
		{429, `{"statusCode":429,"message":"Daily quota","name":"daily_quota_exceeded"}`, "daily_quota_exceeded", nil, false},
		{422, `{"statusCode":422,"message":"Invalid from","name":"invalid_from_address"}`, "invalid_from_address", notijet.ErrInvalidSender, false},
		{403, `{"statusCode":403,"message":"The acme.test domain is not verified.","name":"validation_error"}`, "validation_error", notijet.ErrInvalidSender, false},
		{422, "{\"statusCode\":422,\"message\":\"Invalid `to` field.\",\"name\":\"validation_error\"}", "validation_error", notijet.ErrInvalidRecipient, false},
		{404, `{"statusCode":404,"message":"Template not found","name":"not_found"}`, "not_found", notijet.ErrTemplate, false},
		{409, `{"statusCode":409,"message":"in progress","name":"concurrent_idempotent_requests"}`, "concurrent_idempotent_requests", nil, true},
		{401, `Unauthorized`, "401", notijet.ErrUnauthorized, false},
		{422, `{"statusCode":422,"message":"bad","name":"invalid_attachment"}`, "invalid_attachment", nil, false},
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

	p, _ := server(t, 200, `{}`)
	_, err := p.Send(context.Background(), &notijet.Message{To: []string{"a@x.io"}, Subject: "s", Text: "t"})
	assert.True(t, notijet.IsRetryable(err), "a reply without an id is unexpected")
}

func TestValidation(t *testing.T) {
	p, calls := server(t, 200, ok)
	var to []string
	for i := range 51 {
		to = append(to, fmt.Sprintf("u%d@x.io", i))
	}
	cases := map[string]*notijet.Message{
		"too many To": {To: to, Subject: "s", Text: "t"},
		"no To":       {BCC: []string{"b@x.io"}, Subject: "s", Text: "t"},
		"long ID":     {ID: strings.Repeat("x", 257), To: []string{"a@x.io"}, Subject: "s", Text: "t"},
		"bad tag":     {To: []string{"a@x.io"}, Subject: "s", Text: "t", Metadata: map[string]string{"order id": "1"}},
		"bad value":   {To: []string{"a@x.io"}, Subject: "s", Text: "t", Metadata: map[string]string{"k": "a/b"}},
	}
	for name, msg := range cases {
		_, err := p.Send(context.Background(), msg)
		assert.Error(t, err, name)
		assert.False(t, notijet.IsRetryable(err), name)
	}
	_, err := New("").Send(context.Background(), &notijet.Message{From: "a@x.io", To: []string{"a@x.io"}, Subject: "s", Text: "t"})
	assert.Error(t, err)
	assert.Empty(t, *calls)
}

func TestConformance(t *testing.T) {
	providertest.RunHTTP(t, providertest.HTTPCase{
		Channel: notijet.ChannelEmail,
		New: func(baseURL string, hc *http.Client) notijet.Provider {
			return New("s3cr3t-key", WithBaseURL(baseURL), WithHTTPClient(hc), WithFrom("a@acme.test"))
		},
		Message: func() *notijet.Message { return &notijet.Message{To: []string{"b@x.io"}, Subject: "s", Text: "t"} },
		Secret:  "s3cr3t-key",
	})
}
