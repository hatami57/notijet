package notijet

import (
	"testing"

	"github.com/hatami57/microjet/core/errorx"
	"github.com/stretchr/testify/assert"
)

func TestCheckByChannel(t *testing.T) {
	sms := &Message{To: []string{"0912"}, Template: NewTemplate("otp")}
	assert.NoError(t, Check("p", ChannelSMS, sms), "templates are accepted")
	assert.ErrorIs(t, Check("p", ChannelSMS, &Message{To: []string{"0912"}, Text: "x", BCC: []string{"b"}}), ErrUnsupported)

	assert.NoError(t, Check("p", ChannelEmail, &Message{BCC: []string{"a@x.io"}, Subject: "s", Text: "x"}))
	assert.Error(t, Check("p", ChannelEmail, &Message{To: []string{"a@x.io"}, Text: "x"}), "no subject")

	assert.NoError(t, Check("p", ChannelPush, &Message{To: []string{"tok"}, Text: "x"}))
	err := Check("p", ChannelPush, &Message{Text: "x"})
	assert.True(t, errorx.IsBadRequestError(err))
	assert.Equal(t, "p", errorx.GetError(err).Subject)
	assert.Error(t, Check("p", ChannelWebhook, nil))
}
