package infobip

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hatami57/notijet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type call struct {
	path, auth string
	body       map[string]any
}

func server(t *testing.T, status int, reply string) (*Provider, *[]call) {
	t.Helper()
	var calls []call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		require.NoError(t, json.Unmarshal(b, &body))
		calls = append(calls, call{r.URL.Path, r.Header.Get("Authorization"), body})
		w.WriteHeader(status)
		fmt.Fprint(w, reply)
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL+"/", "key", WithSender("Acme"), WithNotifyURL("https://x.io/dlr")), &calls
}

func TestSend(t *testing.T) {
	p, calls := server(t, 200, `{"bulkId":"b1","messages":[
		{"to":"447700900123","status":{"groupId":1,"groupName":"PENDING","id":26,"name":"PENDING_ACCEPTED"},"messageId":"m1"},
		{"to":"447700900124","status":{"groupId":5,"groupName":"REJECTED","id":51,"name":"REJECTED_DESTINATION"},"messageId":"m2"}]}`)
	res, err := p.Send(context.Background(), &notijet.Message{ID: "o1", To: []string{"+447700900123", "+447700900124"}, Text: "hi"})
	require.NoError(t, err)
	c := (*calls)[0]
	assert.Equal(t, "/sms/2/text/advanced", c.path)
	assert.Equal(t, "App key", c.auth)
	m := c.body["messages"].([]any)[0].(map[string]any)
	assert.Equal(t, "Acme", m["from"])
	assert.Equal(t, "hi", m["text"])
	assert.Equal(t, "o1", m["callbackData"])
	assert.Equal(t, "https://x.io/dlr", m["notifyUrl"])
	assert.Equal(t, []any{map[string]any{"to": "447700900123"}, map[string]any{"to": "447700900124"}}, m["destinations"])
	assert.Equal(t, []string{"m1", "m2"}, res.MessageIDs())
	assert.Equal(t, "REJECTED_DESTINATION", res.Receipts[1].Status)
	assert.Equal(t, "+447700900123", res.Receipts[0].To)
}

func TestErrors(t *testing.T) {
	cases := []struct {
		status   int
		body     string
		code     string
		sentinel error
	}{
		{401, `{"requestError":{"serviceException":{"messageId":"UNAUTHORIZED","text":"Invalid login details"}}}`, "UNAUTHORIZED", notijet.ErrUnauthorized},
		{400, `{"requestError":{"serviceException":{"messageId":"BAD_REQUEST","text":"Bad request"}}}`, "BAD_REQUEST", nil},
		{200, `{"messages":[{"to":"1","status":{"groupId":5,"name":"REJECTED_NOT_ENOUGH_CREDITS","description":"no credit"},"messageId":"m"}]}`, "REJECTED_NOT_ENOUGH_CREDITS", notijet.ErrInsufficientCredit},
		{200, `{"messages":[{"to":"1","status":{"groupId":5,"name":"REJECTED_SENDER"},"messageId":"m"}]}`, "REJECTED_SENDER", notijet.ErrInvalidSender},
		{200, `{"messages":[{"to":"1","status":{"groupId":5,"name":"REJECTED_PREFIX_MISSING"},"messageId":"m"}]}`, "REJECTED_PREFIX_MISSING", notijet.ErrInvalidRecipient},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			p, _ := server(t, tc.status, tc.body)
			_, err := p.Send(context.Background(), &notijet.Message{To: []string{"+447700900123"}, Text: "x"})
			require.Error(t, err)
			if tc.sentinel != nil {
				assert.ErrorIs(t, err, tc.sentinel)
			}
			assert.Equal(t, tc.code, notijet.ProviderCode(err))
			assert.False(t, notijet.IsRetryable(err))
		})
	}

	_, err := New("", "k").Send(context.Background(), &notijet.Message{To: []string{"+447700900123"}, Text: "x"})
	assert.Error(t, err)
	assert.Equal(t, "https://xyz.api.infobip.com", New("xyz.api.infobip.com", "k").baseURL)
}
