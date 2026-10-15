package mimex

import (
	"bytes"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"strings"
	"testing"
	"time"

	"github.com/hatami57/notijet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func parse(t *testing.T, raw []byte) *mail.Message {
	t.Helper()
	m, err := mail.ReadMessage(bytes.NewReader(raw))
	require.NoError(t, err)
	return m
}

func TestBuildPlainText(t *testing.T) {
	date := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	built, err := Build("smtp", &notijet.Message{
		From:    `"Shop" <shop@example.com>`,
		To:      []string{"a@example.com", " "},
		CC:      []string{"c@example.com"},
		BCC:     []string{"hidden@example.com"},
		ReplyTo: []string{"support@example.com"},
		Subject: "سفارش شما ارسال شد",
		Text:    "line1\nline2",
		Headers: map[string]string{"List-Unsubscribe": "<mailto:u@example.com>"},
	}, Options{Date: date})
	require.NoError(t, err)

	assert.Equal(t, "shop@example.com", built.From)
	assert.Equal(t, []string{"a@example.com", "c@example.com", "hidden@example.com"}, built.Recipients)
	assert.True(t, strings.HasSuffix(built.MessageID, "@example.com>"))

	m := parse(t, built.Bytes)
	assert.Equal(t, `"Shop" <shop@example.com>`, m.Header.Get("From"))
	assert.Equal(t, "<a@example.com>", m.Header.Get("To"))
	assert.Equal(t, "", m.Header.Get("Bcc"), "SMTP messages never reveal BCC")
	assert.Equal(t, "<support@example.com>", m.Header.Get("Reply-To"))
	assert.Equal(t, "<mailto:u@example.com>", m.Header.Get("List-Unsubscribe"))
	subject, err := new(mime.WordDecoder).DecodeHeader(m.Header.Get("Subject"))
	require.NoError(t, err)
	assert.Equal(t, "سفارش شما ارسال شد", subject)
	d, err := m.Header.Date()
	require.NoError(t, err)
	assert.True(t, d.Equal(date))
	assert.True(t, strings.HasPrefix(m.Header.Get("Content-Type"), "text/plain"))
	body, _ := io.ReadAll(m.Body)
	assert.Contains(t, string(body), "line1\r\nline2")
}

func TestBuildIncludeBCC(t *testing.T) {
	built, err := Build("gmail", &notijet.Message{
		To: []string{"a@example.com"}, BCC: []string{"b@example.com"}, Subject: "s", Text: "x",
	}, Options{From: "me@example.com", IncludeBCC: true})
	require.NoError(t, err)
	assert.Equal(t, "<b@example.com>", parse(t, built.Bytes).Header.Get("Bcc"))
	assert.Equal(t, "me@example.com", built.From, "falls back to Options.From")
}

func TestBuildMultipart(t *testing.T) {
	built, err := Build("smtp", &notijet.Message{
		From: "me@example.com", To: []string{"a@example.com"}, Subject: "s",
		Text: "plain", HTML: `<p>html <img src="cid:logo"></p>`,
		Attachments: []notijet.Attachment{
			{Filename: "logo.png", ContentID: "logo", Content: []byte("PNG")},
			{Filename: "invoice.pdf", Content: bytes.Repeat([]byte("A"), 200)},
		},
	}, Options{})
	require.NoError(t, err)

	m := parse(t, built.Bytes)
	mediaType, params, err := mime.ParseMediaType(m.Header.Get("Content-Type"))
	require.NoError(t, err)
	assert.Equal(t, "multipart/mixed", mediaType)

	mixed := multipart.NewReader(m.Body, params["boundary"])
	related, err := mixed.NextPart()
	require.NoError(t, err)
	mt, rp, _ := mime.ParseMediaType(related.Header.Get("Content-Type"))
	assert.Equal(t, "multipart/related", mt)

	rel := multipart.NewReader(related, rp["boundary"])
	alt, err := rel.NextPart()
	require.NoError(t, err)
	mt, ap, _ := mime.ParseMediaType(alt.Header.Get("Content-Type"))
	assert.Equal(t, "multipart/alternative", mt)
	altR := multipart.NewReader(alt, ap["boundary"])
	textP, err := altR.NextPart()
	require.NoError(t, err)
	b, _ := io.ReadAll(textP) // multipart decodes quoted-printable
	assert.Equal(t, "plain", string(b))
	htmlP, err := altR.NextPart()
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(htmlP.Header.Get("Content-Type"), "text/html"))

	logo, err := rel.NextPart()
	require.NoError(t, err)
	assert.Equal(t, "<logo>", logo.Header.Get("Content-Id"))
	assert.True(t, strings.HasPrefix(logo.Header.Get("Content-Disposition"), "inline"))
	assert.Equal(t, "image/png", strings.Split(logo.Header.Get("Content-Type"), ";")[0])

	pdf, err := mixed.NextPart()
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(pdf.Header.Get("Content-Disposition"), "attachment"))
	assert.Equal(t, "invoice.pdf", pdf.FileName())
	raw, _ := io.ReadAll(pdf)
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\r\n") {
		assert.LessOrEqual(t, len(line), 76, "base64 lines are wrapped")
	}
}

func TestBuildRejectsBadInput(t *testing.T) {
	base := func() *notijet.Message {
		return &notijet.Message{From: "me@example.com", To: []string{"a@example.com"}, Subject: "s", Text: "x"}
	}

	m := base()
	m.From = ""
	_, err := Build("p", m, Options{})
	assert.Error(t, err, "no sender")

	m = base()
	m.From = "not an address"
	_, err = Build("p", m, Options{})
	assert.ErrorIs(t, err, notijet.ErrInvalidSender)

	m = base()
	m.To = []string{"broken"}
	_, err = Build("p", m, Options{})
	assert.ErrorIs(t, err, notijet.ErrInvalidRecipient)

	m = base()
	m.Subject = "hi\r\nBcc: victim@example.com"
	_, err = Build("p", m, Options{})
	require.NoError(t, err, "subjects are encoded, never raw")

	m = base()
	m.Headers = map[string]string{"X-Bad": "a\r\nBcc: victim@example.com"}
	_, err = Build("p", m, Options{})
	assert.Error(t, err, "header injection")

	m = base()
	m.Headers = map[string]string{"subject": "override"}
	_, err = Build("p", m, Options{})
	assert.Error(t, err, "reserved header")

	m = base()
	m.To = nil
	_, err = Build("p", m, Options{})
	assert.Error(t, err, "no recipient")
}

func TestAttachmentContentTypeWithParams(t *testing.T) {
	msg := &notijet.Message{
		From: "a@x.io", To: []string{"b@x.io"}, Subject: "s", Text: "t",
		Attachments: []notijet.Attachment{
			{Filename: "notes.txt", Content: []byte("hi")},
			{Filename: "page.html", Content: []byte("<p>")},
			{Filename: "data.bin", ContentType: "application/x-custom; version=2", Content: []byte{1}},
			{Filename: "odd", ContentType: "not a type;;", Content: []byte{1}},
		},
	}
	m, err := Build("p", msg, Options{})
	if err != nil {
		t.Fatal(err)
	}
	out := string(m.Bytes)
	for _, want := range []string{
		`Content-Type: text/plain; charset=utf-8; name=notes.txt`,
		`Content-Type: text/html; charset=utf-8; name=page.html`,
		`Content-Type: application/x-custom; name=data.bin; version=2`,
		`Content-Type: application/octet-stream; name=odd`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestContentTypeAndCheckHeaders(t *testing.T) {
	cases := map[string]notijet.Attachment{
		"image/png":                {Filename: "a.png"},
		"application/octet-stream": {Filename: "noext"},
		"application/pdf":          {Filename: "a.png", ContentType: " application/pdf "},
	}
	for want, a := range cases {
		if got := ContentType(a); got != want {
			t.Errorf("ContentType(%+v) = %q, want %q", a, got, want)
		}
	}
	if err := CheckHeaders(map[string]string{"List-Unsubscribe": "<mailto:u@x.io>"}); err != nil {
		t.Error(err)
	}
	for _, bad := range []map[string]string{
		{"Subject": "x"}, {"X-A": "a\r\nBcc: e@x.io"}, {"X A": "v"}, {"": "v"},
	} {
		if CheckHeaders(bad) == nil {
			t.Errorf("CheckHeaders(%q) = nil", bad)
		}
	}
	_, err := Build("p", &notijet.Message{From: "a@x.io", To: []string{"b@x.io"}, Subject: "s", Text: "t",
		Headers: map[string]string{"": "v"}}, Options{})
	if err == nil {
		t.Error("Build accepted an empty header name")
	}
}
