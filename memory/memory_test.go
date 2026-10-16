package memory

import (
	"context"
	"testing"

	"github.com/hatami57/microjet/core/errorx"
	"github.com/hatami57/notijet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSendRecords(t *testing.T) {
	p := New()
	msg := &notijet.Message{To: []string{"0912", " 0935 "}, Template: notijet.NewTemplate("verify", "code", "1234")}
	res, err := p.Send(context.Background(), msg)
	require.NoError(t, err)
	assert.Equal(t, "memory", res.Provider)
	assert.Equal(t, []string{"memory-1", "memory-2"}, res.MessageIDs())
	assert.Equal(t, "0935", res.Receipts[1].To)

	msg.Template.Params[0].Value = "changed"
	assert.Equal(t, "1234", p.Last().Template.Get("code"), "a copy is recorded")
	assert.Equal(t, p.Last(), p.LastTo("0935"))
	last := p.Last()
	last.Template.Params[0].Value = "mutated outside provider"
	last.To[0] = "mutated outside provider"
	assert.Equal(t, "1234", p.Last().Template.Get("code"), "inspection returns a copy")
	assert.Equal(t, "0912", p.Last().To[0], "inspection returns a copy")
	assert.Nil(t, p.LastTo("0999"))
	assert.Equal(t, 1, p.Len())
	assert.Len(t, p.Messages(), 1)

	p.Reset()
	assert.Equal(t, 0, p.Len())
	assert.Nil(t, p.Last())
	res, err = p.Send(context.Background(), &notijet.Message{To: []string{"0912"}, Text: "hi"})
	require.NoError(t, err)
	assert.Equal(t, "memory-1", res.MessageID())
}

func TestValidatesForChannel(t *testing.T) {
	sms := New()
	_, err := sms.Send(context.Background(), &notijet.Message{To: []string{"0912"}})
	assert.True(t, errorx.IsBadRequestError(err))
	assert.Equal(t, "memory", errorx.GetError(err).Subject)
	_, err = sms.Send(context.Background(), &notijet.Message{
		To: []string{"0912"}, Text: "x", Attachments: []notijet.Attachment{{Filename: "a"}},
	})
	assert.ErrorIs(t, err, notijet.ErrUnsupported)
	assert.Equal(t, 0, sms.Calls(), "invalid messages are not counted")

	mail := New(WithName("mail"), WithChannel(notijet.ChannelEmail))
	_, err = mail.Send(context.Background(), &notijet.Message{To: []string{"a@x.io"}, Text: "x"})
	assert.Error(t, err, "email without subject")
	res, err := mail.Send(context.Background(), &notijet.Message{BCC: []string{"a@x.io"}, Subject: "s", Text: "x"})
	require.NoError(t, err)
	assert.Equal(t, "a@x.io", res.Receipts[0].To)
	assert.NotNil(t, mail.LastTo("a@x.io"))

	push := New(WithChannel(notijet.ChannelPush))
	_, err = push.Send(context.Background(), &notijet.Message{Text: "x"})
	assert.Equal(t, "memory", errorx.GetError(err).Subject)
}

func TestScriptedFailures(t *testing.T) {
	p := New()
	boom := notijet.Fault("memory", "boom", nil)
	p.FailNext(nil, boom)
	msg := &notijet.Message{To: []string{"0912"}, Text: "hi"}

	_, err := p.Send(context.Background(), msg)
	assert.NoError(t, err)
	_, err = p.Send(context.Background(), msg)
	assert.Same(t, boom, err)
	_, err = p.Send(context.Background(), msg)
	assert.NoError(t, err)

	p.FailWith(func(m *notijet.Message) error {
		if p.Len() >= 3 {
			return boom
		}
		return nil
	})
	_, err = p.Send(context.Background(), msg)
	assert.NoError(t, err)
	_, err = p.Send(context.Background(), msg)
	assert.Same(t, boom, err)
	assert.Equal(t, 3, p.Len())
	assert.Equal(t, 5, p.Calls())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = p.Send(ctx, msg)
	assert.ErrorIs(t, err, context.Canceled)
}
