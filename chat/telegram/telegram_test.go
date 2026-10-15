package telegram

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hatami57/notijet"
	"github.com/hatami57/notijet/providertest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOptions(t *testing.T) {
	var path string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(b, &body))
		w.Write([]byte(`{"ok":true,"result":{"message_id":5}}`))
	}))
	defer srv.Close()
	p := New("1:T", WithBaseURL(srv.URL), WithHTTPClient(srv.Client()), WithChatID("-100"), WithSilent(), WithoutLinkPreview())
	assert.Equal(t, "telegram", p.Name())
	assert.Equal(t, notijet.ChannelChat, p.Channel())
	res, err := p.Send(context.Background(), &notijet.Message{HTML: "<b>up</b>"})
	require.NoError(t, err)
	assert.Equal(t, "/bot1:T/sendMessage", path)
	assert.Equal(t, "-100", body["chat_id"])
	assert.Equal(t, "HTML", body["parse_mode"])
	assert.Equal(t, true, body["disable_notification"])
	assert.NotNil(t, body["link_preview_options"])
	assert.Equal(t, "telegram", res.Provider)
	assert.Equal(t, "5", res.MessageID())
}

func TestConformance(t *testing.T) {
	providertest.RunHTTP(t, providertest.HTTPCase{
		Channel: notijet.ChannelChat,
		New: func(baseURL string, hc *http.Client) notijet.Provider {
			return New("123:s3cr3t", WithBaseURL(baseURL), WithHTTPClient(hc))
		},
		Message: func() *notijet.Message { return &notijet.Message{To: []string{"-100"}, Text: "hi"} },
		Secret:  "123:s3cr3t",
	})
}
