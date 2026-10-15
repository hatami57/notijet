package notijet

import (
	"context"
	"errors"
	"testing"

	"github.com/hatami57/microjet/core/errorx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewTemplate(t *testing.T) {
	tpl := NewTemplate("verify", "code", "1234", "name", "Ali", "dangling")
	assert.Equal(t, "verify", tpl.ID)
	assert.Equal(t, []Param{{"code", "1234"}, {"name", "Ali"}, {"dangling", ""}}, tpl.Params)
	assert.Equal(t, "Ali", tpl.Get("name"))
	assert.Equal(t, "", tpl.Get("missing"))
	assert.Equal(t, []string{"1234", "Ali", ""}, tpl.Values())
	assert.Equal(t, map[string]string{"code": "1234", "name": "Ali", "dangling": ""}, tpl.Map())
}

func TestMessageValidate(t *testing.T) {
	var nilMsg *Message
	assert.True(t, errorx.IsBadRequestError(nilMsg.Validate()))
	assert.Error(t, (&Message{Text: "hi"}).Validate(), "no recipient")
	assert.Error(t, (&Message{To: []string{"  ", ""}, Text: "hi"}).Validate(), "blank recipients")
	assert.Error(t, (&Message{To: []string{"a"}}).Validate(), "no body")
	assert.Error(t, (&Message{To: []string{"a"}, Template: &Template{}}).Validate(), "template without ID")
	assert.NoError(t, (&Message{To: []string{"a"}, Text: "hi"}).Validate())
	assert.NoError(t, (&Message{To: []string{"a"}, Template: NewTemplate("t")}).Validate())
	assert.Equal(t, []string{"a", "b"}, (&Message{To: []string{" a ", "", "b"}}).Recipients())
}

func TestMessageClone(t *testing.T) {
	m := &Message{
		To:          []string{"a"},
		Template:    NewTemplate("t", "k", "v"),
		Attachments: []Attachment{{Filename: "f", Content: []byte("x")}},
		Data:        map[string]string{"k": "v"},
	}
	c := m.Clone()
	c.To[0] = "changed"
	c.Template.Params[0].Value = "changed"
	c.Attachments[0].Content[0] = 'y'
	c.Data["k"] = "changed"
	assert.Equal(t, "a", m.To[0])
	assert.Equal(t, "v", m.Template.Params[0].Value)
	assert.Equal(t, byte('x'), m.Attachments[0].Content[0])
	assert.Equal(t, "v", m.Data["k"])
	assert.Nil(t, (*Message)(nil).Clone())
}

func TestResultIDs(t *testing.T) {
	var nilRes *Result
	assert.Equal(t, "", nilRes.MessageID())
	r := &Result{Receipts: []Receipt{{MessageID: "1"}, {}, {MessageID: "3"}}}
	assert.Equal(t, "1", r.MessageID())
	assert.Equal(t, []string{"1", "3"}, r.MessageIDs())
}

func TestErrorsCarryCategoryAndSentinel(t *testing.T) {
	err := Rejected("kavenegar", "418", "credit", ErrInsufficientCredit)
	assert.True(t, errorx.IsBusinessError(err))
	assert.ErrorIs(t, err, ErrInsufficientCredit)
	assert.Equal(t, "kavenegar", errorx.GetError(err).Subject)
	assert.Equal(t, "418", ProviderCode(err))
	assert.False(t, IsRetryable(err))

	th := Throttled("twilio", "429", "")
	assert.ErrorIs(t, th, ErrRateLimited)
	assert.True(t, IsRetryable(th))

	f := Fault("smtp", "", errors.New("conn reset"))
	assert.True(t, errorx.IsInternalError(f))
	assert.True(t, IsRetryable(f))
	assert.Equal(t, "", ProviderCode(f))

	assert.False(t, IsRetryable(Invalid("x", "bad")))
	assert.False(t, IsRetryable(Unsupported("x", "HTML")))
	assert.ErrorIs(t, Unsupported("x", "HTML"), ErrUnsupported)
	assert.ErrorIs(t, InvalidRecipient("x", "bad"), ErrInvalidRecipient)
	assert.ErrorIs(t, InvalidSender("x", "bad"), ErrInvalidSender)
	assert.False(t, IsRetryable(nil))
	assert.False(t, IsRetryable(context.Canceled))
	assert.True(t, IsRetryable(errors.New("custom provider failure")))
	assert.Equal(t, "", ProviderCode(errors.New("plain")))
}

func TestCheckSMS(t *testing.T) {
	msg := &Message{To: []string{"0912"}, Text: "hi"}
	useTpl, err := CheckSMS("p", msg, true)
	require.NoError(t, err)
	assert.False(t, useTpl)

	withTpl := &Message{To: []string{"0912"}, Text: "fallback", Template: NewTemplate("otp", "code", "1")}
	useTpl, err = CheckSMS("p", withTpl, true)
	require.NoError(t, err)
	assert.True(t, useTpl, "template providers send the template")
	useTpl, err = CheckSMS("p", withTpl, false)
	require.NoError(t, err)
	assert.False(t, useTpl, "text providers fall back to Text")

	_, err = CheckSMS("p", &Message{To: []string{"0912"}, Template: NewTemplate("otp")}, false)
	assert.ErrorIs(t, err, ErrUnsupported)

	_, err = CheckSMS("p", &Message{To: []string{"0912"}, HTML: "<b>x</b>"}, false)
	assert.ErrorIs(t, err, ErrUnsupported)

	_, err = CheckSMS("p", &Message{To: []string{"0912"}, Text: "x", Attachments: []Attachment{{Filename: "a"}}}, false)
	assert.ErrorIs(t, err, ErrUnsupported)

	_, err = CheckSMS("p", &Message{To: []string{"0912"}, Text: "x", BCC: []string{"b"}}, false)
	assert.ErrorIs(t, err, ErrUnsupported)

	_, err = CheckSMS("myprovider", &Message{Text: "x"}, false)
	require.Error(t, err)
	assert.Equal(t, "myprovider", errorx.GetError(err).Subject, "validation errors carry the provider subject")
}

func TestCheckEmail(t *testing.T) {
	ok := &Message{BCC: []string{"a@x.io"}, Subject: "s", HTML: "<p>x</p>"}
	useTpl, err := CheckEmail("p", ok, false)
	require.NoError(t, err)
	assert.False(t, useTpl)

	_, err = CheckEmail("p", &Message{Subject: "s", Text: "x"}, false)
	assert.Error(t, err, "no recipient")
	_, err = CheckEmail("p", &Message{To: []string{"a@x.io"}, Text: "x"}, false)
	assert.Error(t, err, "no subject")
	_, err = CheckEmail("p", &Message{To: []string{"a@x.io"}, Subject: "s"}, false)
	assert.Error(t, err, "no body")
	_, err = CheckEmail("p", &Message{To: []string{"a@x.io"}, Subject: "s", Text: "x",
		Attachments: []Attachment{{Content: []byte("x")}}}, false)
	assert.Error(t, err, "attachment without name")

	tpl := &Message{To: []string{"a@x.io"}, Template: NewTemplate("welcome")}
	useTpl, err = CheckEmail("p", tpl, true)
	require.NoError(t, err)
	assert.True(t, useTpl, "a template supplies its own subject and body")
	_, err = CheckEmail("p", tpl, false)
	assert.ErrorIs(t, err, ErrUnsupported)
	_, err = CheckEmail("p", &Message{To: []string{"a@x.io"}, Template: &Template{}}, true)
	assert.Error(t, err)
}

func TestCheckChat(t *testing.T) {
	assert.NoError(t, CheckChat("p", &Message{Text: "x"}, false))
	assert.Error(t, CheckChat("p", &Message{Text: "x"}, true))
	assert.NoError(t, CheckChat("p", &Message{To: []string{"1"}, HTML: "<b>x</b>"}, true))
	assert.ErrorIs(t, CheckChat("p", &Message{Template: NewTemplate("t")}, false), ErrUnsupported)
	assert.Error(t, CheckChat("p", &Message{}, false))
	assert.Error(t, CheckChat("p", nil, false))
}
