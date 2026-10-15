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
		Attachments: []notijet.Attachment{{Filename: "invoice.pdf"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "dev-mail", res.Provider)
	assert.NotEmpty(t, res.MessageID())
	out := buf.String()
	for _, s := range []string{"notijet: message", "a@x.io", "Welcome", "hello there", "invoice.pdf", res.MessageID()} {
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
