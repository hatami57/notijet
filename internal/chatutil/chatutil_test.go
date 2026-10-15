package chatutil

import (
	"testing"

	"github.com/hatami57/notijet"
	"github.com/stretchr/testify/assert"
)

func TestCheckWebhook(t *testing.T) {
	assert.NoError(t, CheckWebhook("p", &notijet.Message{Text: "x"}))
	assert.ErrorIs(t, CheckWebhook("p", &notijet.Message{To: []string{"#a"}, Text: "x"}), notijet.ErrUnsupported)
	assert.ErrorIs(t, CheckWebhook("p", &notijet.Message{HTML: "<b>x</b>"}), notijet.ErrUnsupported)
	assert.Error(t, CheckWebhook("p", nil))
}

func TestText(t *testing.T) {
	assert.Equal(t, "x", Text(&notijet.Message{Text: "x"}))
	assert.Equal(t, "Title\n\nx", Text(&notijet.Message{Subject: " Title ", Text: "x"}))
}
