package postmark

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
	path, token string
	body        map[string]any
}

func server(t *testing.T, status int, reply string) (*Provider, *[]call) {
	t.Helper()
	var calls []call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		require.NoError(t, json.Unmarshal(b, &body))
		calls = append(calls, call{r.URL.Path, r.Header.Get("X-Postmark-Server-Token"), body})
		w.WriteHeader(status)
		fmt.Fprint(w, reply)
	}))
	t.Cleanup(srv.Close)
	return New("tok", WithBaseURL(srv.URL), WithFrom("Acme <no-reply@acme.test>"), WithMessageStream("broadcast")), &calls
}

const ok = `{"To":"a@x.io","SubmittedAt":"2024-10-15T10:00:00Z","MessageID":"b7bc2f4a","ErrorCode":0,"Message":"OK"}`

func TestSend(t *testing.T) {
	p, calls := server(t, 200, ok)
	res, err := p.Send(context.Background(), &notijet.Message{
		To:          []string{"Ali Rezaei <a@x.io>", "b@x.io"},
		CC:          []string{"c@x.io"},
		BCC:         []string{"d@x.io"},
		ReplyTo:     []string{"r@x.io"},
		Subject:     "Hello",
		Text:        "plain",
		HTML:        "<p>html</p>",
		Headers:     map[string]string{"X-B": "2", "X-A": "1"},
		Metadata:    map[string]string{"order": "42"},
		Attachments: []notijet.Attachment{{Filename: "a.pdf", Content: []byte("PDF")}, {ContentID: "logo", ContentType: "image/png", Content: []byte("PNG")}},
	})
	require.NoError(t, err)
	assert.Equal(t, "b7bc2f4a", res.MessageID())

	c := (*calls)[0]
	assert.Equal(t, "/email", c.path)
	assert.Equal(t, "tok", c.token)
	b := c.body
	assert.Equal(t, "Acme <no-reply@acme.test>", b["From"])
	assert.Equal(t, "Ali Rezaei <a@x.io>, b@x.io", b["To"])
	assert.Equal(t, "c@x.io", b["Cc"])
	assert.Equal(t, "d@x.io", b["Bcc"])
	assert.Equal(t, "r@x.io", b["ReplyTo"])
	assert.Equal(t, "Hello", b["Subject"])
	assert.Equal(t, "plain", b["TextBody"])
	assert.Equal(t, "<p>html</p>", b["HtmlBody"])
	assert.Equal(t, "broadcast", b["MessageStream"])
	assert.Equal(t, []any{map[string]any{"Name": "X-A", "Value": "1"}, map[string]any{"Name": "X-B", "Value": "2"}}, b["Headers"])
	assert.Equal(t, map[string]any{"order": "42"}, b["Metadata"])
	assert.Equal(t, []any{
		map[string]any{"Name": "a.pdf", "Content": "UERG", "ContentType": "application/pdf"},
		map[string]any{"Name": "logo", "Content": "UE5H", "ContentType": "image/png", "ContentID": "cid:logo"},
	}, b["Attachments"])
}

func TestTemplates(t *testing.T) {
	p, calls := server(t, 200, ok)
	_, err := p.Send(context.Background(), &notijet.Message{To: []string{"a@x.io"}, Subject: "ignored", Template: notijet.NewTemplate("12345", "name", "Ali")})
	require.NoError(t, err)
	_, err = p.Send(context.Background(), &notijet.Message{To: []string{"a@x.io"}, Template: notijet.NewTemplate("welcome")})
	require.NoError(t, err)

	byID, byAlias := (*calls)[0], (*calls)[1]
	assert.Equal(t, "/email/withTemplate", byID.path)
	assert.Equal(t, float64(12345), byID.body["TemplateId"])
	assert.Nil(t, byID.body["TemplateAlias"])
	assert.Nil(t, byID.body["Subject"], "the template supplies the subject")
	assert.Equal(t, map[string]any{"name": "Ali"}, byID.body["TemplateModel"])
	assert.Equal(t, "welcome", byAlias.body["TemplateAlias"])
	assert.Nil(t, byAlias.body["TemplateId"])
}

func TestErrors(t *testing.T) {
	cases := []struct {
		status, code int
		sentinel     error
		retry        bool
	}{
		{401, 10, notijet.ErrUnauthorized, false},
		{422, 400, notijet.ErrInvalidSender, false},
		{422, 405, notijet.ErrInsufficientCredit, false},
		{422, 406, notijet.ErrInvalidRecipient, false},
		{422, 1101, notijet.ErrTemplate, false},
		{422, 300, nil, false},
		{422, 101, nil, true},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprint(tc.code), func(t *testing.T) {
			p, _ := server(t, tc.status, fmt.Sprintf(`{"ErrorCode":%d,"Message":"err"}`, tc.code))
			_, err := p.Send(context.Background(), &notijet.Message{To: []string{"a@x.io"}, Subject: "s", Text: "t"})
			require.Error(t, err)
			if tc.sentinel != nil {
				assert.ErrorIs(t, err, tc.sentinel)
			}
			assert.Equal(t, fmt.Sprint(tc.code), notijet.ProviderCode(err))
			assert.Equal(t, tc.retry, notijet.IsRetryable(err))
		})
	}

	p, _ := server(t, 401, "Unauthorized")
	_, err := p.Send(context.Background(), &notijet.Message{To: []string{"a@x.io"}, Subject: "s", Text: "t"})
	assert.ErrorIs(t, err, notijet.ErrUnauthorized)

	for _, body := range []string{`{"ErrorCode":0}`, `{}`} {
		p, _ = server(t, 200, body)
		_, err = p.Send(context.Background(), &notijet.Message{To: []string{"a@x.io"}, Subject: "s", Text: "t"})
		assert.True(t, notijet.IsRetryable(err), body)
	}
	p, _ = server(t, 404, `{}`)
	_, err = p.Send(context.Background(), &notijet.Message{To: []string{"a@x.io"}, Subject: "s", Text: "t"})
	assert.Equal(t, "404", notijet.ProviderCode(err))
}

func TestValidation(t *testing.T) {
	p, calls := server(t, 200, ok)
	var to []string
	for i := range 51 {
		to = append(to, fmt.Sprintf("u%d@x.io", i))
	}
	_, err := p.Send(context.Background(), &notijet.Message{To: to, Subject: "s", Text: "t"})
	assert.ErrorContains(t, err, "50 recipients")
	_, err = New("").Send(context.Background(), &notijet.Message{From: "a@x.io", To: []string{"a@x.io"}, Subject: "s", Text: "t"})
	assert.Error(t, err)
	assert.Empty(t, *calls)
}

func TestConformance(t *testing.T) {
	providertest.RunHTTP(t, providertest.HTTPCase{
		Channel: notijet.ChannelEmail,
		New: func(baseURL string, hc *http.Client) notijet.Provider {
			return New("s3cr3t-token", WithBaseURL(baseURL), WithHTTPClient(hc), WithFrom("a@acme.test"))
		},
		Message: func() *notijet.Message { return &notijet.Message{To: []string{"b@x.io"}, Subject: "s", Text: "t"} },
		Secret:  "s3cr3t-token",
	})
}
