package smsutil

import (
	"testing"

	"github.com/hatami57/notijet"
	"github.com/hatami57/notijet/phone"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecipients(t *testing.T) {
	to, err := Recipients("p", &notijet.Message{To: []string{"+989121234567", "09121234567", " ", "09351234567"}}, phone.IranMobile)
	require.NoError(t, err)
	assert.Equal(t, []string{"09121234567", "09351234567"}, to)

	_, err = Recipients("p", &notijet.Message{To: []string{"0912", "09121234567"}}, phone.IranMobile)
	assert.ErrorIs(t, err, notijet.ErrInvalidRecipient)
	assert.Contains(t, err.Error(), "0912")
}

func TestSenderAndFlag(t *testing.T) {
	s, err := Sender("p", &notijet.Message{From: " 3000 "}, "1000", true)
	require.NoError(t, err)
	assert.Equal(t, "3000", s)
	s, _ = Sender("p", &notijet.Message{}, "1000", true)
	assert.Equal(t, "1000", s)
	_, err = Sender("p", &notijet.Message{}, "", true)
	assert.ErrorIs(t, err, notijet.ErrInvalidSender)
	s, err = Sender("p", &notijet.Message{}, "", false)
	assert.NoError(t, err)
	assert.Empty(t, s)

	assert.True(t, Flag(&notijet.Message{Data: map[string]string{"x": "TRUE"}}, "x"))
	assert.False(t, Flag(&notijet.Message{}, "x"))
}

func TestIsGSM7(t *testing.T) {
	assert.True(t, IsGSM7("Hello, world! {€} @£$ 123\n"))
	assert.False(t, IsGSM7("سلام"))
	assert.False(t, IsGSM7("emoji 🙂"))
}
