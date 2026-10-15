package botapi

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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type call struct {
	path string
	body map[string]any
}

func server(t *testing.T, status int, reply string, cfg Config) (*Client, *[]call) {
	t.Helper()
	var calls []call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		require.NoError(t, json.Unmarshal(b, &body))
		calls = append(calls, call{r.URL.Path, body})
		w.WriteHeader(status)
		fmt.Fprint(w, reply)
	}))
	t.Cleanup(srv.Close)
	cfg.Provider, cfg.BaseURL = "bot", srv.URL+"/"
	if cfg.Token == "" {
		cfg.Token = "123:ABC"
	}
	return New(cfg), &calls
}

const ok = `{"ok":true,"result":{"message_id":77,"chat":{"id":-100}}}`

func TestSendToEachChat(t *testing.T) {
	c, calls := server(t, 200, ok, Config{ParseMode: true, Silent: true, NoLinkPreview: true})
	res, err := c.Send(context.Background(), &notijet.Message{To: []string{"-100", "@news"}, Subject: "Deploy", Text: "done"})
	require.NoError(t, err)
	require.Len(t, *calls, 2)
	first := (*calls)[0]
	assert.Equal(t, "/bot123:ABC/sendMessage", first.path)
	assert.Equal(t, "-100", first.body["chat_id"])
	assert.Equal(t, "Deploy\n\ndone", first.body["text"])
	assert.Equal(t, true, first.body["disable_notification"])
	assert.Equal(t, map[string]any{"is_disabled": true}, first.body["link_preview_options"])
	assert.Nil(t, first.body["parse_mode"])
	assert.Equal(t, "@news", (*calls)[1].body["chat_id"])
	assert.Equal(t, []string{"77", "77"}, res.MessageIDs())
	assert.Equal(t, "@news", res.Receipts[1].To)
}

func TestDefaultChatAndParseModes(t *testing.T) {
	c, calls := server(t, 200, ok, Config{ParseMode: true, DefaultChat: "42"})
	_, err := c.Send(context.Background(), &notijet.Message{HTML: "<b>hi</b>", Text: "hi"})
	require.NoError(t, err)
	b := (*calls)[0].body
	assert.Equal(t, "42", b["chat_id"])
	assert.Equal(t, "<b>hi</b>", b["text"], "HTML wins over Text where supported")
	assert.Equal(t, "HTML", b["parse_mode"])
	assert.Nil(t, b["disable_notification"])

	_, err = c.Send(context.Background(), &notijet.Message{Text: "*hi*", Data: map[string]string{"parse_mode": "MarkdownV2"}})
	require.NoError(t, err)
	assert.Equal(t, "MarkdownV2", (*calls)[1].body["parse_mode"])
}

func TestWithoutParseMode(t *testing.T) {
	c, calls := server(t, 200, ok, Config{DefaultChat: "42"})
	_, err := c.Send(context.Background(), &notijet.Message{HTML: "<b>hi</b>"})
	assert.ErrorIs(t, err, notijet.ErrUnsupported)
	_, err = c.Send(context.Background(), &notijet.Message{Text: "hi", Data: map[string]string{"parse_mode": "HTML"}})
	assert.ErrorIs(t, err, notijet.ErrUnsupported)
	_, err = c.Send(context.Background(), &notijet.Message{Text: "**hi**"})
	require.NoError(t, err)
	assert.Len(t, *calls, 1)
}

func TestValidation(t *testing.T) {
	c, calls := server(t, 200, ok, Config{ParseMode: true})
	_, err := c.Send(context.Background(), &notijet.Message{Text: "hi"})
	assert.Error(t, err, "no recipient and no default chat")
	_, err = c.Send(context.Background(), &notijet.Message{To: []string{"1"}, Text: strings.Repeat("ب", 4097)})
	assert.ErrorContains(t, err, "4096")
	_, err = c.Send(context.Background(), &notijet.Message{To: []string{"1"}, Text: strings.Repeat("ب", 4096)})
	assert.NoError(t, err, "the limit counts characters, not bytes")
	_, err = New(Config{Provider: "bot"}).Send(context.Background(), &notijet.Message{To: []string{"1"}, Text: "x"})
	assert.ErrorContains(t, err, "token")
	assert.Len(t, *calls, 1)
}

func TestErrors(t *testing.T) {
	cases := []struct {
		status   int
		body     string
		sentinel error
		retry    bool
	}{
		{401, `{"ok":false,"error_code":401,"description":"Unauthorized"}`, notijet.ErrUnauthorized, false},
		{404, `{"ok":false,"error_code":404,"description":"Not Found"}`, notijet.ErrUnauthorized, false},
		{403, `{"ok":false,"error_code":403,"description":"Forbidden: bot was blocked by the user"}`, notijet.ErrInvalidRecipient, false},
		{400, `{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`, notijet.ErrInvalidRecipient, false},
		{400, `{"ok":false,"error_code":400,"description":"Bad Request: can't parse entities"}`, nil, false},
		{200, `{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 5","parameters":{"retry_after":5}}`, notijet.ErrRateLimited, true},
		{200, `{"ok":false,"error_code":502,"description":"Bad Gateway"}`, nil, true},
		{400, `{"ok":false,"description":"no code"}`, nil, false},
		{403, `Forbidden`, notijet.ErrInvalidRecipient, false},
	}
	for _, tc := range cases {
		t.Run(tc.body, func(t *testing.T) {
			c, _ := server(t, tc.status, tc.body, Config{})
			_, err := c.Send(context.Background(), &notijet.Message{To: []string{"1"}, Text: "x"})
			require.Error(t, err)
			if tc.sentinel != nil {
				assert.ErrorIs(t, err, tc.sentinel)
			}
			assert.Equal(t, tc.retry, notijet.IsRetryable(err))
			assert.NotContains(t, err.Error(), "123:ABC", "the token never appears in errors")
		})
	}

	c, _ := server(t, 200, `{"ok":true,"result":{}}`, Config{})
	_, err := c.Send(context.Background(), &notijet.Message{To: []string{"1"}, Text: "x"})
	assert.True(t, notijet.IsRetryable(err), "success without message_id is unexpected")
}
