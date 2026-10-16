package emailutil

import (
	"net/mail"
	"testing"

	"github.com/hatami57/notijet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrepare(t *testing.T) {
	msg := &notijet.Message{
		To:      []string{"A <a@x.io>", " "},
		CC:      []string{"c@x.io"},
		BCC:     []string{"b@x.io"},
		ReplyTo: []string{"r@x.io"},
		Subject: "s",
		Text:    "t",
	}
	env, useTemplate, err := Prepare("p", msg, "Acme <no-reply@acme.test>", true)
	require.NoError(t, err)
	assert.False(t, useTemplate)
	assert.Equal(t, "no-reply@acme.test", env.From.Address)
	assert.Equal(t, []string{"a@x.io"}, Emails(env.To))
	assert.Equal(t, 3, env.Count())
	assert.Equal(t, "r@x.io", env.ReplyTo[0].Address)

	msg.Template = notijet.NewTemplate("welcome")
	_, useTemplate, err = Prepare("p", msg, "a@acme.test", true)
	require.NoError(t, err)
	assert.True(t, useTemplate)
}

func TestPrepareErrors(t *testing.T) {
	base := func() *notijet.Message {
		return &notijet.Message{From: "a@x.io", To: []string{"b@x.io"}, Subject: "s", Text: "t"}
	}
	cases := map[string]struct {
		mutate   func(*notijet.Message)
		sentinel error
	}{
		"no from":       {func(m *notijet.Message) { m.From = "" }, nil},
		"bad from":      {func(m *notijet.Message) { m.From = "nope" }, notijet.ErrInvalidSender},
		"bad to":        {func(m *notijet.Message) { m.To = []string{"nope"} }, notijet.ErrInvalidRecipient},
		"bad cc":        {func(m *notijet.Message) { m.CC = []string{"nope"} }, notijet.ErrInvalidRecipient},
		"bad bcc":       {func(m *notijet.Message) { m.BCC = []string{"nope"} }, notijet.ErrInvalidRecipient},
		"bad reply-to":  {func(m *notijet.Message) { m.ReplyTo = []string{"nope"} }, nil},
		"bad header":    {func(m *notijet.Message) { m.Headers = map[string]string{"X": "a\nb"} }, nil},
		"no subject":    {func(m *notijet.Message) { m.Subject = "" }, nil},
		"template only": {func(m *notijet.Message) { m.Text = ""; m.Template = notijet.NewTemplate("t") }, notijet.ErrUnsupported},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			m := base()
			tc.mutate(m)
			_, _, err := Prepare("p", m, "", false)
			require.Error(t, err)
			if tc.sentinel != nil {
				assert.ErrorIs(t, err, tc.sentinel)
			}
			assert.False(t, notijet.IsRetryable(err))
		})
	}
}

func TestDisplay(t *testing.T) {
	assert.Equal(t, "a@x.io", Display(&mail.Address{Address: "a@x.io"}))
	assert.Equal(t, "Ali Rezaei <a@x.io>", Display(&mail.Address{Name: "Ali Rezaei", Address: "a@x.io"}))
	assert.Equal(t, "علی <a@x.io>", Display(&mail.Address{Name: "علی", Address: "a@x.io"}), "UTF-8 names are kept as is")
	assert.Equal(t, `"Acme, Inc. \"HQ\"" <a@x.io>`, Display(&mail.Address{Name: `Acme, Inc. "HQ"`, Address: "a@x.io"}))
	assert.Equal(t, []string{"a@x.io"}, DisplayList([]*mail.Address{{Address: "a@x.io"}}))
	assert.Equal(t, []notijet.Receipt{{MessageID: "id", Status: "queued"}}, Receipt("id", "queued"))
}

func TestValidTag(t *testing.T) {
	assert.True(t, ValidTag("order_id-1"))
	for _, s := range []string{"", "a b", "a.b", "علی", string(make([]byte, 257))} {
		assert.False(t, ValidTag(s), s)
	}
}
