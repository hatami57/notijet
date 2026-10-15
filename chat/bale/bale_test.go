package bale

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

func TestSend(t *testing.T) {
	var path string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(b, &body))
		w.Write([]byte(`{"ok":true,"result":{"message_id":9}}`))
	}))
	defer srv.Close()
	p := New("1:T", WithBaseURL(srv.URL), WithHTTPClient(srv.Client()), WithChatID("42"))
	assert.Equal(t, "bale", p.Name())
	assert.Equal(t, notijet.ChannelChat, p.Channel())
	res, err := p.Send(context.Background(), &notijet.Message{Text: "سفارش *ارسال* شد"})
	require.NoError(t, err)
	assert.Equal(t, "/bot1:T/sendMessage", path)
	assert.Equal(t, "42", body["chat_id"])
	assert.Equal(t, "سفارش *ارسال* شد", body["text"])
	assert.Nil(t, body["parse_mode"], "Bale has no parse_mode")
	assert.Equal(t, "9", res.MessageID())

	_, err = p.Send(context.Background(), &notijet.Message{HTML: "<b>x</b>"})
	assert.ErrorIs(t, err, notijet.ErrUnsupported)
	assert.Equal(t, "https://tapi.bale.ai", DefaultBaseURL)
}

func TestConformance(t *testing.T) {
	providertest.RunHTTP(t, providertest.HTTPCase{
		Channel: notijet.ChannelChat,
		New: func(baseURL string, hc *http.Client) notijet.Provider {
			return New("123:s3cr3t", WithBaseURL(baseURL), WithHTTPClient(hc))
		},
		Message: func() *notijet.Message { return &notijet.Message{To: []string{"42"}, Text: "hi"} },
		Secret:  "123:s3cr3t",
	})
}
