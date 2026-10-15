package logger

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/hatami57/notijet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSendLogs(t *testing.T) {
	var buf bytes.Buffer
	l := slog.New(slog.NewTextHandler(&buf, nil))
	p := New(l, WithName("dev-mail"), WithChannel(notijet.ChannelEmail))
	assert.Equal(t, "dev-mail", p.Name())
	assert.Equal(t, notijet.ChannelEmail, p.Channel())

	res, err := p.Send(context.Background(), &notijet.Message{
		To:          []string{"a@x.io"},
		Subject:     "Welcome",
		Text:        "hello there",
		Attachments: []notijet.Attachment{{Filename: "invoice.pdf"}, {ContentID: "logo"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "dev-mail", res.Provider)
	assert.NotEmpty(t, res.MessageID())
	out := buf.String()
	for _, s := range []string{"notijet: message", "a@x.io", "Welcome", "hello there", "invoice.pdf", "cid:logo", res.MessageID()} {
		assert.Contains(t, out, s)
	}
}

func TestLevelAndValidation(t *testing.T) {
	var buf bytes.Buffer
	l := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	p := New(l, WithLevel(slog.LevelDebug))
	_, err := p.Send(context.Background(), &notijet.Message{To: []string{"0912"}, Template: notijet.NewTemplate("otp", "code", "1234")})
	require.NoError(t, err)
	assert.Empty(t, buf.String(), "debug is below the handler level")

	_, err = p.Send(context.Background(), &notijet.Message{To: []string{"0912"}})
	assert.Error(t, err)
	assert.NotNil(t, New(nil))
}

func TestLogsEveryField(t *testing.T) {
	var buf bytes.Buffer
	p := New(slog.New(slog.NewTextHandler(&buf, nil)), WithChannel(notijet.ChannelPush))
	_, err := p.Send(context.Background(), &notijet.Message{
		ID: "m1", To: []string{"tok"}, From: "app", Subject: "title", Text: "body",
		Data: map[string]string{"k": "v"}, Metadata: map[string]string{"tag": "t1"},
	})
	require.NoError(t, err)
	for _, s := range []string{"id=m1", "from=app", "subject=title", "data=map[k:v]", "metadata=map[tag:t1]"} {
		assert.Contains(t, buf.String(), s)
	}

	buf.Reset()
	mail := New(slog.New(slog.NewTextHandler(&buf, nil)), WithChannel(notijet.ChannelEmail))
	_, err = mail.Send(context.Background(), &notijet.Message{
		To: []string{"a@x.io"}, CC: []string{"c@x.io"}, ReplyTo: []string{"r@x.io"}, Subject: "s", HTML: "<b>hi</b>",
	})
	require.NoError(t, err)
	for _, s := range []string{"cc=[c@x.io]", "reply_to=[r@x.io]", "html=<b>hi</b>"} {
		assert.Contains(t, buf.String(), s)
	}
}

func TestPushValidationAndCancel(t *testing.T) {
	p := New(slog.New(slog.DiscardHandler), WithChannel(notijet.ChannelPush))
	_, err := p.Send(context.Background(), &notijet.Message{Text: "x"})
	assert.Error(t, err, "no recipient")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = p.Send(ctx, &notijet.Message{To: []string{"tok"}, Text: "x"})
	assert.ErrorIs(t, err, context.Canceled)
}
