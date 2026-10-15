package mailgun

import (
	"context"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hatami57/notijet"
	"github.com/hatami57/notijet/providertest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type filePart struct {
	field, filename, contentType, content string
}

type call struct {
	path, user, pass string
	fields           map[string][]string
	files            []filePart
}

func server(t *testing.T, status int, reply string) (*Provider, *[]call) {
	t.Helper()
	var calls []call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := call{path: r.URL.Path, fields: map[string][]string{}}
		c.user, c.pass, _ = r.BasicAuth()
		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		require.NoError(t, err)
		mr := multipart.NewReader(r.Body, params["boundary"])
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			require.NoError(t, err)
			b, _ := io.ReadAll(part)
			if part.FileName() != "" {
				c.files = append(c.files, filePart{part.FormName(), part.FileName(), part.Header.Get("Content-Type"), string(b)})
			} else {
				c.fields[part.FormName()] = append(c.fields[part.FormName()], string(b))
			}
		}
		calls = append(calls, c)
		w.WriteHeader(status)
		fmt.Fprint(w, reply)
	}))
	t.Cleanup(srv.Close)
	return New("mg.acme.test", "key-1", WithBaseURL(srv.URL), WithFrom("Acme <no-reply@mg.acme.test>")), &calls
}

const queued = `{"id":"<20241015.1@mg.acme.test>","message":"Queued. Thank you."}`

func TestSend(t *testing.T) {
	p, calls := server(t, 200, queued)
	res, err := p.Send(context.Background(), &notijet.Message{
		To:          []string{"Ali <a@x.io>", "b@x.io"},
		CC:          []string{"c@x.io"},
		BCC:         []string{"d@x.io"},
		ReplyTo:     []string{"r@x.io"},
		Subject:     "Hello",
		Text:        "plain",
		HTML:        `<img src="cid:logo">`,
		Headers:     map[string]string{"X-Campaign": "fall"},
		Metadata:    map[string]string{"order": "42"},
		Attachments: []notijet.Attachment{{Filename: "a.pdf", Content: []byte("PDF")}, {ContentID: "<logo>", Content: []byte("PNG"), ContentType: "image/png"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "<20241015.1@mg.acme.test>", res.MessageID())

	c := (*calls)[0]
	assert.Equal(t, "/v3/mg.acme.test/messages", c.path)
	assert.Equal(t, "api", c.user)
	assert.Equal(t, "key-1", c.pass)
	f := c.fields
	assert.Equal(t, []string{"Acme <no-reply@mg.acme.test>"}, f["from"])
	assert.Equal(t, []string{"Ali <a@x.io>", "b@x.io"}, f["to"])
	assert.Equal(t, []string{"c@x.io"}, f["cc"])
	assert.Equal(t, []string{"d@x.io"}, f["bcc"])
	assert.Equal(t, []string{"r@x.io"}, f["h:Reply-To"])
	assert.Equal(t, []string{"Hello"}, f["subject"])
	assert.Equal(t, []string{"plain"}, f["text"])
	assert.Equal(t, []string{`<img src="cid:logo">`}, f["html"])
	assert.Equal(t, []string{"fall"}, f["h:X-Campaign"])
	assert.Equal(t, []string{"42"}, f["v:order"])
	assert.Equal(t, []filePart{
		{"attachment", "a.pdf", "application/pdf", "PDF"},
		{"inline", "logo", "image/png", "PNG"},
	}, c.files)
}

func TestTemplate(t *testing.T) {
	p, calls := server(t, 200, queued)
	_, err := p.Send(context.Background(), &notijet.Message{
		To:       []string{"a@x.io"},
		Template: notijet.NewTemplate("welcome", "name", "Ali"),
	})
	require.NoError(t, err)
	f := (*calls)[0].fields
	assert.Equal(t, []string{"welcome"}, f["template"])
	assert.Equal(t, []string{`{"name":"Ali"}`}, f["t:variables"])
	assert.Nil(t, f["subject"])
	assert.Nil(t, f["text"])
}

func TestErrors(t *testing.T) {
	cases := []struct {
		status   int
		body     string
		sentinel error
	}{
		{401, `Forbidden`, notijet.ErrUnauthorized},
		{402, `{"message":"Payment required"}`, notijet.ErrInsufficientCredit},
		{404, `{"message":"Domain not found: mg.acme.test"}`, notijet.ErrInvalidSender},
		{400, `{"message":"'from' parameter is not a valid address. please check documentation"}`, notijet.ErrInvalidSender},
		{400, `{"message":"'to' parameter is not a valid address. please check documentation"}`, notijet.ErrInvalidRecipient},
		{400, `{"message":"template not found"}`, notijet.ErrTemplate},
		{400, `{"message":"Need at least one of 'text' or 'html' parameters specified"}`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.body, func(t *testing.T) {
			p, _ := server(t, tc.status, tc.body)
			_, err := p.Send(context.Background(), &notijet.Message{To: []string{"a@x.io"}, Subject: "s", Text: "t"})
			require.Error(t, err)
			if tc.sentinel != nil {
				assert.ErrorIs(t, err, tc.sentinel)
			}
			assert.Equal(t, fmt.Sprint(tc.status), notijet.ProviderCode(err))
			assert.False(t, notijet.IsRetryable(err))
		})
	}

	p, _ := server(t, 200, `{"message":"Queued"}`)
	_, err := p.Send(context.Background(), &notijet.Message{To: []string{"a@x.io"}, Subject: "s", Text: "t"})
	assert.True(t, notijet.IsRetryable(err), "a reply without an id is unexpected")

	_, err = New("", "k").Send(context.Background(), &notijet.Message{From: "a@x.io", To: []string{"a@x.io"}, Subject: "s", Text: "t"})
	assert.Error(t, err)
}

func TestEscapeQuotes(t *testing.T) {
	assert.Equal(t, `a\"b\\c`, escapeQuotes("a\"b\\c\r\n"))
}

func TestConformance(t *testing.T) {
	providertest.RunHTTP(t, providertest.HTTPCase{
		Channel: notijet.ChannelEmail,
		New: func(baseURL string, hc *http.Client) notijet.Provider {
			return New("mg.acme.test", "s3cr3t-key", WithBaseURL(baseURL), WithHTTPClient(hc), WithFrom("a@acme.test"))
		},
		Message: func() *notijet.Message { return &notijet.Message{To: []string{"b@x.io"}, Subject: "s", Text: "t"} },
		Secret:  "s3cr3t-key",
	})
}
