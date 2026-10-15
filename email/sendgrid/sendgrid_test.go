package sendgrid

import (
	"context"
	"encoding/json"
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
	body       map[string]any
}

func server(t *testing.T, status int, reply string) (*Provider, *[]call) {
	t.Helper()
	var calls []call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		require.NoError(t, json.Unmarshal(b, &body))
		calls = append(calls, call{r.URL.Path, r.Header.Get("Authorization"), body})
		w.Header().Set("X-Message-Id", "sg-123")
		w.WriteHeader(status)
		fmt.Fprint(w, reply)
	}))
	t.Cleanup(srv.Close)
	return New("SG.key", WithBaseURL(srv.URL), WithFrom("Acme <no-reply@acme.test>")), &calls
}

func full() *notijet.Message {
	return &notijet.Message{
		To:      []string{"Ali <a@x.io>"},
		CC:      []string{"c@x.io", "A@x.io"},
		BCC:     []string{"b@x.io"},
		ReplyTo: []string{"r@x.io"},
		Subject: "Hello",
		Text:    "plain",
		HTML:    "<p>html</p>",
		Headers: map[string]string{"List-Unsubscribe": "<mailto:u@acme.test>"},
		Attachments: []notijet.Attachment{
			{Filename: "a.pdf", Content: []byte("PDF")},
			{ContentID: "<logo>", ContentType: "image/png", Content: []byte("PNG")},
		},
		Metadata: map[string]string{"order": "42"},
	}
}

func TestSend(t *testing.T) {
	p, calls := server(t, 202, "")
	res, err := p.Send(context.Background(), full())
	require.NoError(t, err)
	assert.Equal(t, "sg-123", res.MessageID())
	assert.Equal(t, "sendgrid", res.Provider)

	c := (*calls)[0]
	assert.Equal(t, "/v3/mail/send", c.path)
	assert.Equal(t, "Bearer SG.key", c.auth)
	b := c.body
	assert.Equal(t, map[string]any{"email": "no-reply@acme.test", "name": "Acme"}, b["from"])
	assert.Equal(t, map[string]any{"email": "r@x.io"}, b["reply_to"])
	assert.Equal(t, "Hello", b["subject"])
	assert.Equal(t, []any{
		map[string]any{"type": "text/plain", "value": "plain"},
		map[string]any{"type": "text/html", "value": "<p>html</p>"},
	}, b["content"])
	pers := b["personalizations"].([]any)[0].(map[string]any)
	assert.Equal(t, []any{map[string]any{"email": "a@x.io", "name": "Ali"}}, pers["to"])
	assert.Equal(t, []any{map[string]any{"email": "c@x.io"}}, pers["cc"], "duplicates of earlier recipients are dropped")
	assert.Equal(t, []any{map[string]any{"email": "b@x.io"}}, pers["bcc"])
	assert.Equal(t, map[string]any{"List-Unsubscribe": "<mailto:u@acme.test>"}, b["headers"])
	assert.Equal(t, map[string]any{"order": "42"}, b["custom_args"])
	atts := b["attachments"].([]any)
	assert.Equal(t, map[string]any{"content": "UERG", "type": "application/pdf", "filename": "a.pdf", "disposition": "attachment"}, atts[0])
	assert.Equal(t, map[string]any{"content": "UE5H", "type": "image/png", "filename": "logo", "disposition": "inline", "content_id": "logo"}, atts[1])
}

func TestTemplateAndReplyToList(t *testing.T) {
	p, calls := server(t, 202, "")
	_, err := p.Send(context.Background(), &notijet.Message{
		From:     "x@acme.test",
		To:       []string{"a@x.io"},
		ReplyTo:  []string{"r1@x.io", "r2@x.io"},
		Template: notijet.NewTemplate("d-abc", "name", "Ali"),
		Text:     "fallback for other providers",
	})
	require.NoError(t, err)
	b := (*calls)[0].body
	assert.Equal(t, "d-abc", b["template_id"])
	assert.Nil(t, b["content"], "the template supplies the content")
	assert.Nil(t, b["subject"])
	assert.Len(t, b["reply_to_list"], 2)
	pers := b["personalizations"].([]any)[0].(map[string]any)
	assert.Equal(t, map[string]any{"name": "Ali"}, pers["dynamic_template_data"])
}

func TestErrors(t *testing.T) {
	cases := []struct {
		status   int
		body     string
		sentinel error
	}{
		{401, `{"errors":[{"message":"The provided authorization grant is invalid, expired, or revoked","field":null}]}`, notijet.ErrUnauthorized},
		{401, `{"errors":[{"message":"Maximum credits exceeded","field":null}]}`, notijet.ErrInsufficientCredit},
		{403, `{"errors":[{"message":"The from address does not match a verified Sender Identity.","field":"from"}]}`, notijet.ErrInvalidSender},
		{400, `{"errors":[{"message":"The template_id must be a valid GUID","field":"template_id"}]}`, notijet.ErrTemplate},
		{400, `{"errors":[{"message":"Does not contain a valid address.","field":"personalizations.0.to.0.email"}]}`, notijet.ErrInvalidRecipient},
		{413, `payload too large`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.body, func(t *testing.T) {
			p, _ := server(t, tc.status, tc.body)
			_, err := p.Send(context.Background(), full())
			require.Error(t, err)
			if tc.sentinel != nil {
				assert.ErrorIs(t, err, tc.sentinel)
			}
			assert.Equal(t, fmt.Sprint(tc.status), notijet.ProviderCode(err))
			assert.False(t, notijet.IsRetryable(err))
		})
	}
}

func TestValidation(t *testing.T) {
	p, calls := server(t, 202, "")
	_, err := p.Send(context.Background(), &notijet.Message{BCC: []string{"b@x.io"}, Subject: "s", Text: "t"})
	assert.ErrorContains(t, err, "To recipient")
	_, err = New("").Send(context.Background(), full())
	assert.Error(t, err)
	_, err = New("k").Send(context.Background(), &notijet.Message{To: []string{"a@x.io"}, Subject: "s", Text: "t"})
	assert.ErrorContains(t, err, "From")
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
