package brevo

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
	path, key string
	body      map[string]any
}

func server(t *testing.T, status int, reply string) (*Provider, *[]call) {
	t.Helper()
	var calls []call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		require.NoError(t, json.Unmarshal(b, &body))
		calls = append(calls, call{r.URL.Path, r.Header.Get("api-key"), body})
		w.WriteHeader(status)
		fmt.Fprint(w, reply)
	}))
	t.Cleanup(srv.Close)
	return New("xkeysib-1", WithBaseURL(srv.URL+"/v3"), WithFrom("Acme <no-reply@acme.test>")), &calls
}

const ok = `{"messageId":"<202410151000.1@smtp-relay.mailin.fr>"}`

func TestSend(t *testing.T) {
	p, calls := server(t, 201, ok)
	res, err := p.Send(context.Background(), &notijet.Message{
		To:          []string{"Ali <a@x.io>"},
		CC:          []string{"c@x.io"},
		BCC:         []string{"b@x.io"},
		ReplyTo:     []string{"Support <r@x.io>"},
		Subject:     "Hello",
		Text:        "plain",
		HTML:        "<p>html</p>",
		Headers:     map[string]string{"X-Campaign": "fall"},
		Metadata:    map[string]string{"order": "42", "kind": "welcome"},
		Attachments: []notijet.Attachment{{Filename: "a.pdf", Content: []byte("PDF")}},
	})
	require.NoError(t, err)
	assert.Equal(t, "<202410151000.1@smtp-relay.mailin.fr>", res.MessageID())

	c := (*calls)[0]
	assert.Equal(t, "/v3/smtp/email", c.path)
	assert.Equal(t, "xkeysib-1", c.key)
	b := c.body
	assert.Equal(t, map[string]any{"email": "no-reply@acme.test", "name": "Acme"}, b["sender"])
	assert.Equal(t, []any{map[string]any{"email": "a@x.io", "name": "Ali"}}, b["to"])
	assert.Equal(t, []any{map[string]any{"email": "c@x.io"}}, b["cc"])
	assert.Equal(t, []any{map[string]any{"email": "b@x.io"}}, b["bcc"])
	assert.Equal(t, map[string]any{"email": "r@x.io", "name": "Support"}, b["replyTo"])
	assert.Equal(t, "Hello", b["subject"])
	assert.Equal(t, "plain", b["textContent"])
	assert.Equal(t, "<p>html</p>", b["htmlContent"])
	assert.Equal(t, map[string]any{"X-Campaign": "fall", "X-Mailin-custom": "kind:welcome|order:42"}, b["headers"])
	assert.Equal(t, []any{map[string]any{"content": "UERG", "name": "a.pdf"}}, b["attachment"])
}

func TestTemplate(t *testing.T) {
	p, calls := server(t, 201, ok)
	_, err := p.Send(context.Background(), &notijet.Message{To: []string{"a@x.io"}, Template: notijet.NewTemplate("7", "name", "Ali")})
	require.NoError(t, err)
	b := (*calls)[0].body
	assert.Equal(t, float64(7), b["templateId"])
	assert.Equal(t, map[string]any{"name": "Ali"}, b["params"])
	assert.Nil(t, b["htmlContent"])

	_, err = p.Send(context.Background(), &notijet.Message{To: []string{"a@x.io"}, Template: notijet.NewTemplate("welcome")})
	assert.ErrorIs(t, err, notijet.ErrTemplate)
}

func TestErrors(t *testing.T) {
	cases := []struct {
		status   int
		body     string
		code     string
		sentinel error
		template bool
	}{
		{401, `{"code":"unauthorized","message":"Key not found"}`, "unauthorized", notijet.ErrUnauthorized, false},
		{402, `{"code":"not_enough_credits","message":"no credits"}`, "not_enough_credits", notijet.ErrInsufficientCredit, false},
		{400, `{"code":"invalid_parameter","message":"Sender is invalid / inactive"}`, "invalid_parameter", notijet.ErrInvalidSender, false},
		{400, `{"code":"invalid_parameter","message":"email is not valid in to"}`, "invalid_parameter", notijet.ErrInvalidRecipient, false},
		{400, `{"code":"missing_parameter","message":"Template params missing"}`, "missing_parameter", notijet.ErrTemplate, false},
		{404, `{"code":"document_not_found","message":"Template ID does not exist"}`, "document_not_found", notijet.ErrTemplate, true},
		{403, `{"code":"something_new","message":"nope"}`, "something_new", notijet.ErrUnauthorized, false},
		{400, `{"code":"duplicate_request","message":"same request"}`, "duplicate_request", nil, false},
		{400, `not json`, "400", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			p, _ := server(t, tc.status, tc.body)
			msg := &notijet.Message{To: []string{"a@x.io"}, Subject: "s", Text: "t"}
			if tc.template {
				msg.Template = notijet.NewTemplate("9")
			}
			_, err := p.Send(context.Background(), msg)
			require.Error(t, err)
			if tc.sentinel != nil {
				assert.ErrorIs(t, err, tc.sentinel)
			}
			assert.Equal(t, tc.code, notijet.ProviderCode(err))
			assert.False(t, notijet.IsRetryable(err))
		})
	}

	p, _ := server(t, 201, `{}`)
	_, err := p.Send(context.Background(), &notijet.Message{To: []string{"a@x.io"}, Subject: "s", Text: "t"})
	assert.True(t, notijet.IsRetryable(err), "a reply without messageId is unexpected")
}

func TestValidation(t *testing.T) {
	p, calls := server(t, 201, ok)
	base := func() *notijet.Message { return &notijet.Message{To: []string{"a@x.io"}, Subject: "s", Text: "t"} }
	cases := map[string]func(*notijet.Message){
		"no To":          func(m *notijet.Message) { m.To, m.BCC = nil, []string{"b@x.io"} },
		"two Reply-To":   func(m *notijet.Message) { m.ReplyTo = []string{"r1@x.io", "r2@x.io"} },
		"inline":         func(m *notijet.Message) { m.Attachments = []notijet.Attachment{{ContentID: "logo"}} },
		"metadata pipe":  func(m *notijet.Message) { m.Metadata = map[string]string{"k": "a|b"} },
		"metadata colon": func(m *notijet.Message) { m.Metadata = map[string]string{"k:x": "v"} },
		"header conflicts": func(m *notijet.Message) {
			m.Metadata = map[string]string{"k": "v"}
			m.Headers = map[string]string{"x-mailin-custom": "a"}
		},
	}
	for name, mutate := range cases {
		m := base()
		mutate(m)
		_, err := p.Send(context.Background(), m)
		assert.Error(t, err, name)
		assert.False(t, notijet.IsRetryable(err), name)
	}
	_, err := p.Send(context.Background(), &notijet.Message{To: []string{"a@x.io"}, Subject: "s", Text: "t", Attachments: []notijet.Attachment{{ContentID: "c"}}})
	assert.ErrorIs(t, err, notijet.ErrUnsupported)
	_, err = New("").Send(context.Background(), &notijet.Message{From: "a@x.io", To: []string{"a@x.io"}, Subject: "s", Text: "t"})
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
