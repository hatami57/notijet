package smsir

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

func server(t *testing.T, status int, reply func(body map[string]any) string) (*Provider, *[]call) {
	t.Helper()
	var calls []call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		require.NoError(t, json.Unmarshal(b, &body))
		calls = append(calls, call{r.URL.Path, r.Header.Get("X-API-KEY"), body})
		w.WriteHeader(status)
		fmt.Fprint(w, reply(body))
	}))
	t.Cleanup(srv.Close)
	return New("secret", WithBaseURL(srv.URL), WithSender("30007732000000")), &calls
}

func bulkOK(body map[string]any) string {
	n := len(body["mobiles"].([]any))
	ids := make([]int, n)
	for i := range ids {
		ids[i] = 500 + i
	}
	b, _ := json.Marshal(ids)
	return fmt.Sprintf(`{"status":1,"message":"موفق","data":{"packId":"a1","messageIds":%s,"cost":%d}}`, b, n)
}

func TestBulk(t *testing.T) {
	p, calls := server(t, 200, bulkOK)
	res, err := p.Send(context.Background(), &notijet.Message{To: []string{"+989121234567", "09351234567"}, Text: "hi"})
	require.NoError(t, err)
	require.Len(t, *calls, 1)
	c := (*calls)[0]
	assert.Equal(t, "/v1/send/bulk", c.path)
	assert.Equal(t, "secret", c.key)
	assert.Equal(t, float64(30007732000000), c.body["lineNumber"])
	assert.Equal(t, "hi", c.body["messageText"])
	assert.Equal(t, []any{"09121234567", "09351234567"}, c.body["mobiles"])
	assert.Equal(t, []string{"500", "501"}, res.MessageIDs())
	assert.Equal(t, "smsir", res.Provider)
}

func TestBulkBatchesAndSender(t *testing.T) {
	p, calls := server(t, 200, bulkOK)
	var to []string
	for i := range 250 {
		to = append(to, fmt.Sprintf("0912%07d", i))
	}
	res, err := p.Send(context.Background(), &notijet.Message{To: to, Text: "hi", From: "3000"})
	require.NoError(t, err)
	assert.Len(t, *calls, 3)
	assert.Len(t, res.MessageIDs(), 250)
	assert.Equal(t, float64(3000), (*calls)[0].body["lineNumber"])

	_, err = New("k").Send(context.Background(), &notijet.Message{To: []string{"09121234567"}, Text: "x"})
	assert.ErrorIs(t, err, notijet.ErrInvalidSender, "bulk needs a line")
	_, err = New("k", WithSender("abc")).Send(context.Background(), &notijet.Message{To: []string{"09121234567"}, Text: "x"})
	assert.ErrorIs(t, err, notijet.ErrInvalidSender)
}

func TestVerify(t *testing.T) {
	p, calls := server(t, 200, func(map[string]any) string {
		return `{"status":1,"message":"موفق","data":{"messageId":8899,"cost":1.5}}`
	})
	res, err := p.Send(context.Background(), &notijet.Message{
		To:       []string{"09121234567"},
		Template: notijet.NewTemplate("123456", "CODE", "52341"),
	})
	require.NoError(t, err)
	c := (*calls)[0]
	assert.Equal(t, "/v1/send/verify", c.path)
	assert.Equal(t, "09121234567", c.body["mobile"])
	assert.Equal(t, float64(123456), c.body["templateId"])
	assert.Equal(t, []any{map[string]any{"name": "CODE", "value": "52341"}}, c.body["parameters"])
	assert.Equal(t, "8899", res.MessageID())
	assert.Equal(t, "1.5", res.Receipts[0].Cost)

	_, err = p.Send(context.Background(), &notijet.Message{To: []string{"09121234567"}, Template: notijet.NewTemplate("otp")})
	assert.ErrorIs(t, err, notijet.ErrTemplate)
}

func TestErrors(t *testing.T) {
	cases := []struct {
		http, status int
		sentinel     error
		retry        bool
	}{
		{401, 10, notijet.ErrUnauthorized, false},
		{400, 101, notijet.ErrInvalidSender, false},
		{400, 102, notijet.ErrInsufficientCredit, false},
		{400, 104, notijet.ErrInvalidRecipient, false},
		{400, 113, notijet.ErrTemplate, false},
		{200, 20, notijet.ErrRateLimited, true},
		{200, 0, nil, true},
		{400, 109, nil, false},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			p, _ := server(t, tc.http, func(map[string]any) string {
				return fmt.Sprintf(`{"status":%d,"message":"err","data":null}`, tc.status)
			})
			_, err := p.Send(context.Background(), &notijet.Message{To: []string{"09121234567"}, Text: "x"})
			require.Error(t, err)
			if tc.sentinel != nil {
				assert.ErrorIs(t, err, tc.sentinel)
			}
			assert.Equal(t, tc.retry, notijet.IsRetryable(err))
			assert.Equal(t, fmt.Sprint(tc.status), notijet.ProviderCode(err))
		})
	}

	p, _ := server(t, 404, func(map[string]any) string { return `{}` })
	_, err := p.Send(context.Background(), &notijet.Message{To: []string{"09121234567"}, Text: "x"})
	assert.Equal(t, "404", notijet.ProviderCode(err))

	p, _ = server(t, 200, func(map[string]any) string { return `{"status":1,"message":"ok"}` })
	_, err = p.Send(context.Background(), &notijet.Message{To: []string{"09121234567"}, Text: "x"})
	assert.True(t, notijet.IsRetryable(err), "success without data is unexpected")
}

func TestValidationBeforeNetwork(t *testing.T) {
	p, calls := server(t, 200, bulkOK)
	_, err := p.Send(context.Background(), &notijet.Message{To: []string{"+14155550100"}, Text: "x"})
	assert.ErrorIs(t, err, notijet.ErrInvalidRecipient, "SMS.ir sends to Iranian mobiles only")
	_, err = New("").Send(context.Background(), &notijet.Message{To: []string{"09121234567"}, Text: "x"})
	assert.Error(t, err)
	assert.Empty(t, *calls)

	res, err := p.Send(context.Background(), &notijet.Message{To: []string{"09121234567"}, Text: "x"})
	require.NoError(t, err)
	assert.Equal(t, "1", res.Receipts[0].Cost, "a single-recipient bulk send reports its cost")
}
