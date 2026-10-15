package plivo

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
	path, user string
	body       map[string]any
}

func server(t *testing.T, status int, reply string) (*Provider, *[]call) {
	t.Helper()
	var calls []call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		require.NoError(t, json.Unmarshal(b, &body))
		u, _, _ := r.BasicAuth()
		calls = append(calls, call{r.URL.Path, u, body})
		w.WriteHeader(status)
		fmt.Fprint(w, reply)
	}))
	t.Cleanup(srv.Close)
	return New("MA123", "token", WithBaseURL(srv.URL), WithSender("+14155550100")), &calls
}

func TestSend(t *testing.T) {
	p, calls := server(t, 202, `{"api_id":"x","message":"message(s) queued","message_uuid":["u1","u3"],"invalid_number":["15550000"]}`)
	res, err := p.Send(context.Background(), &notijet.Message{To: []string{"+447700900123", "+15550000", "+447700900124"}, Text: "hi"})
	require.NoError(t, err)
	c := (*calls)[0]
	assert.Equal(t, "/Account/MA123/Message/", c.path)
	assert.Equal(t, "MA123", c.user)
	assert.Equal(t, "447700900123<15550000<447700900124", c.body["dst"])
	assert.Equal(t, "+14155550100", c.body["src"])
	assert.Equal(t, "hi", c.body["text"])
	require.Len(t, res.Receipts, 3)
	assert.Equal(t, "u1", res.Receipts[0].MessageID)
	assert.Equal(t, "invalid", res.Receipts[1].Status)
	assert.Equal(t, "u3", res.Receipts[2].MessageID)
}

func TestPowerpack(t *testing.T) {
	p, calls := server(t, 202, `{"message_uuid":["u1"]}`)
	_, err := p.Send(context.Background(), &notijet.Message{
		To: []string{"+447700900123"}, From: "8e33a96e-4ab5-4c0b-b8a0-3e8f1fd6e0b1", Text: "hi",
	})
	require.NoError(t, err)
	assert.Equal(t, "8e33a96e-4ab5-4c0b-b8a0-3e8f1fd6e0b1", (*calls)[0].body["powerpack_uuid"])
	assert.Nil(t, (*calls)[0].body["src"])
}

func TestErrors(t *testing.T) {
	cases := []struct {
		status   int
		body     string
		sentinel error
	}{
		{401, `{"error":"authentication failed"}`, notijet.ErrUnauthorized},
		{400, `{"api_id":"x","error":"insufficient credit"}`, notijet.ErrInsufficientCredit},
		{400, `{"error":"src parameter not valid"}`, notijet.ErrInvalidSender},
		{400, `{"error":"dst parameter not valid"}`, notijet.ErrInvalidRecipient},
		{202, `{"message_uuid":[],"invalid_number":["447700900123"]}`, notijet.ErrInvalidRecipient},
		{401, `Unauthorized`, notijet.ErrUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.body, func(t *testing.T) {
			p, _ := server(t, tc.status, tc.body)
			_, err := p.Send(context.Background(), &notijet.Message{To: []string{"+447700900123"}, Text: "x"})
			assert.ErrorIs(t, err, tc.sentinel)
			assert.False(t, notijet.IsRetryable(err))
		})
	}
}
