package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hatami57/notijet"
	"github.com/hatami57/notijet/providertest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type call struct {
	path, query string
	body        map[string]any
}

func server(t *testing.T, status int, reply string, opts ...Option) (*Provider, *[]call) {
	t.Helper()
	var calls []call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		require.NoError(t, json.Unmarshal(b, &body))
		calls = append(calls, call{r.URL.Path, r.URL.RawQuery, body})
		w.WriteHeader(status)
		fmt.Fprint(w, reply)
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL+"/api/webhooks/1/s3cr3t?thread_id=9", opts...), &calls
}

const ok = `{"id":"1234567890","channel_id":"55","content":"x"}`

func TestSend(t *testing.T) {
	p, calls := server(t, 200, ok, WithUsername("Deploy bot"), WithAvatarURL("https://x.io/a.png"))
	res, err := p.Send(context.Background(), &notijet.Message{Subject: "Deploy", Text: "**api** is live @everyone"})
	require.NoError(t, err)
	c := (*calls)[0]
	assert.Equal(t, "/api/webhooks/1/s3cr3t", c.path)
	assert.Equal(t, "thread_id=9&wait=true", c.query, "existing query parameters are kept")
	assert.Equal(t, "Deploy\n\n**api** is live @everyone", c.body["content"])
	assert.Equal(t, "Deploy bot", c.body["username"])
	assert.Equal(t, "https://x.io/a.png", c.body["avatar_url"])
	assert.Equal(t, map[string]any{"parse": []any{}}, c.body["allowed_mentions"], "mentions ping no one by default")
	assert.Equal(t, "1234567890", res.MessageID())
	assert.Equal(t, "discord", res.Provider)
	assert.Equal(t, notijet.ChannelChat, p.Channel())
}

func TestMentionsAndEmbeds(t *testing.T) {
	p, calls := server(t, 200, ok, WithMentions())
	_, err := p.Send(context.Background(), &notijet.Message{Text: "hi", Data: map[string]string{"embeds": `[{"title":"t"}]`}})
	require.NoError(t, err)
	b := (*calls)[0].body
	assert.Nil(t, b["allowed_mentions"])
	assert.Equal(t, []any{map[string]any{"title": "t"}}, b["embeds"])
}

func TestValidation(t *testing.T) {
	p, calls := server(t, 200, ok)
	cases := map[string]*notijet.Message{
		"recipients": {To: []string{"#x"}, Text: "x"},
		"html only":  {HTML: "<b>x</b>"},
		"too long":   {Text: strings.Repeat("ب", 2001)},
		"bad embeds": {Text: "x", Data: map[string]string{"embeds": `{"title":"t"}`}},
	}
	for name, msg := range cases {
		_, err := p.Send(context.Background(), msg)
		assert.Error(t, err, name)
		assert.False(t, notijet.IsRetryable(err), name)
	}
	_, err := p.Send(context.Background(), &notijet.Message{Text: strings.Repeat("ب", 2000)})
	assert.NoError(t, err, "the limit counts characters")
	_, err = New("").Send(context.Background(), &notijet.Message{Text: "x"})
	assert.Error(t, err)
	_, err = New("http://[::1").Send(context.Background(), &notijet.Message{Text: "x"})
	assert.Error(t, err)
	assert.Len(t, *calls, 1)
}

func TestErrors(t *testing.T) {
	cases := []struct {
		status   int
		body     string
		code     string
		sentinel error
	}{
		{404, `{"message":"Unknown Webhook","code":10015}`, "10015", notijet.ErrUnauthorized},
		{401, `{"message":"Invalid Webhook Token","code":50027}`, "50027", notijet.ErrUnauthorized},
		{400, `{"message":"Cannot send an empty message","code":50006}`, "50006", nil},
		{400, `{"message":"Invalid Form Body","code":50035,"errors":{}}`, "50035", nil},
		{403, `forbidden`, "403", notijet.ErrUnauthorized},
		{400, `{"message":"bad"}`, "400", nil},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			p, _ := server(t, tc.status, tc.body)
			_, err := p.Send(context.Background(), &notijet.Message{Text: "x"})
			require.Error(t, err)
			if tc.sentinel != nil {
				assert.ErrorIs(t, err, tc.sentinel)
			}
			assert.Equal(t, tc.code, notijet.ProviderCode(err))
			assert.False(t, notijet.IsRetryable(err))
			assert.NotContains(t, err.Error(), "s3cr3t")
		})
	}
}

func TestConformance(t *testing.T) {
	providertest.RunHTTP(t, providertest.HTTPCase{
		Channel: notijet.ChannelChat,
		New: func(baseURL string, hc *http.Client) notijet.Provider {
			return New(baseURL+"/api/webhooks/1/s3cr3t", WithHTTPClient(hc))
		},
		Message:           func() *notijet.Message { return &notijet.Message{Text: "hi"} },
		Secret:            "s3cr3t",
		RecipientOptional: true,
	})
}
