package ippanel

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
	path, key string
	body      map[string]any
}

func server(t *testing.T, status int, reply string) (*Provider, *[]call) {
	t.Helper()
	var calls []call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		require.NoError(t, json.Unmarshal(b, &body))
		calls = append(calls, call{r.URL.Path, r.Header.Get("Apikey"), body})
		w.WriteHeader(status)
		fmt.Fprint(w, reply)
	}))
	t.Cleanup(srv.Close)
	return New("secret", WithBaseURL(srv.URL+"/api/v1"), WithSender("+983000505")), &calls
}

const ok = `{"status":"OK","code":200,"error_message":"","data":{"message_id":7788}}`

func TestWebservice(t *testing.T) {
	p, calls := server(t, 200, ok)
	res, err := p.Send(context.Background(), &notijet.Message{To: []string{"09121234567", "+447700900123"}, Text: "hello"})
	require.NoError(t, err)
	c := (*calls)[0]
	assert.Equal(t, "/api/v1/sms/send/webservice/single", c.path)
	assert.Equal(t, "secret", c.key)
	assert.Equal(t, "+983000505", c.body["sender"])
	assert.Equal(t, []any{"+989121234567", "+447700900123"}, c.body["recipient"])
	assert.Equal(t, "hello", c.body["message"])
	assert.Equal(t, map[string]any{"summary": "hello", "count_recipient": "2"}, c.body["description"])
	assert.Equal(t, "7788", res.MessageID())
	assert.Len(t, res.Receipts, 1)
}

func TestPattern(t *testing.T) {
	p, calls := server(t, 201, ok)
	res, err := p.Send(context.Background(), &notijet.Message{
		To:       []string{"09121234567", "09351234567"},
		From:     "+9810001",
		Template: notijet.NewTemplate("p8x7", "code", "52341"),
	})
	require.NoError(t, err)
	require.Len(t, *calls, 2)
	b := (*calls)[1].body
	assert.Equal(t, "/api/v1/sms/pattern/normal/send", (*calls)[1].path)
	assert.Equal(t, "p8x7", b["code"])
	assert.Equal(t, "+9810001", b["sender"])
	assert.Equal(t, "+989351234567", b["recipient"])
	assert.Equal(t, map[string]any{"code": "52341"}, b["variable"])
	assert.Equal(t, "+989351234567", res.Receipts[1].To)
}

func TestErrors(t *testing.T) {
	text := &notijet.Message{To: []string{"09121234567"}, Text: "x"}
	tpl := &notijet.Message{To: []string{"09121234567"}, Template: notijet.NewTemplate("p")}

	p, _ := server(t, 401, `{"status":"ERROR","code":401,"error_message":"invalid api key","data":null}`)
	_, err := p.Send(context.Background(), text)
	assert.ErrorIs(t, err, notijet.ErrUnauthorized)
	assert.Equal(t, "401", notijet.ProviderCode(err))

	p, _ = server(t, 422, `{"status":"ERROR","code":422,"error_message":"","data":{"error":"your credit is not enough"}}`)
	_, err = p.Send(context.Background(), text)
	assert.ErrorIs(t, err, notijet.ErrInsufficientCredit)

	p, _ = server(t, 422, `{"status":"ERROR","code":422,"data":{"error":{"recipient":["invalid number"]}}}`)
	_, err = p.Send(context.Background(), text)
	assert.Contains(t, err.Error(), "recipient: invalid number")
	assert.False(t, notijet.IsRetryable(err))

	p, _ = server(t, 404, `{"status":"ERROR","code":404,"error_message":"not found","data":null}`)
	_, err = p.Send(context.Background(), tpl)
	assert.ErrorIs(t, err, notijet.ErrTemplate)

	p, _ = server(t, 403, `forbidden`)
	_, err = p.Send(context.Background(), text)
	assert.ErrorIs(t, err, notijet.ErrUnauthorized)

	p, _ = server(t, 200, `{"status":"OK","code":200,"data":{}}`)
	_, err = p.Send(context.Background(), text)
	assert.True(t, notijet.IsRetryable(err))

	_, err = New("k").Send(context.Background(), text)
	assert.ErrorIs(t, err, notijet.ErrInvalidSender)
}
