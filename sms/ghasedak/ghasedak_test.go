package ghasedak

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
		calls = append(calls, call{r.URL.Path, r.Header.Get("ApiKey"), body})
		w.WriteHeader(status)
		fmt.Fprint(w, reply(body))
	}))
	t.Cleanup(srv.Close)
	return New("secret", WithBaseURL(srv.URL)), &calls
}

func TestSingle(t *testing.T) {
	p, calls := server(t, 200, func(body map[string]any) string {
		return fmt.Sprintf(`{"Data":{"Receptor":%q,"Cost":1732,"MessageId":"2387933"},"IsSuccess":true,"StatusCode":200,"Message":"ok"}`, body["receptor"])
	})
	res, err := p.Send(context.Background(), &notijet.Message{ID: "ord7", To: []string{"09121234567", "09351234567"}, Text: "hi", From: "3000"})
	require.NoError(t, err)
	require.Len(t, *calls, 2)
	c := (*calls)[1]
	assert.Equal(t, "/SendSingleSMS", c.path)
	assert.Equal(t, "secret", c.key)
	assert.Equal(t, "09351234567", c.body["receptor"])
	assert.Equal(t, "3000", c.body["lineNumber"])
	assert.Equal(t, "hi", c.body["message"])
	assert.Equal(t, "ord7-2", c.body["clientReferenceId"])
	assert.Equal(t, []string{"2387933", "2387933"}, res.MessageIDs())
	assert.Equal(t, "1732", res.Receipts[0].Cost)
	assert.Equal(t, "09351234567", res.Receipts[1].To)

	_, err = p.Send(context.Background(), &notijet.Message{To: []string{"09121234567"}, Text: "hi"})
	require.NoError(t, err)
	_, hasLine := (*calls)[2].body["lineNumber"]
	assert.False(t, hasLine, "no line: Ghasedak picks the account default")
}

func TestOTP(t *testing.T) {
	p, calls := server(t, 200, func(map[string]any) string {
		return `{"Data":{"LineNumber":"1000","Items":[{"Receptor":"09121234567","Cost":940,"MessageId":"11"},{"Receptor":"09351234567","Cost":940,"MessageId":"12"}],"Cost":1880},"IsSuccess":true,"StatusCode":200}`
	})
	res, err := p.Send(context.Background(), &notijet.Message{
		To:       []string{"09121234567", "09351234567"},
		Template: notijet.NewTemplate("verify", "Code", "52341"),
		Data:     map[string]string{"voice": "true"},
	})
	require.NoError(t, err)
	require.Len(t, *calls, 1)
	b := (*calls)[0].body
	assert.Equal(t, "/SendOtpSMS", (*calls)[0].path)
	assert.Equal(t, "verify", b["templateName"])
	assert.Equal(t, true, b["isVoice"])
	assert.Equal(t, []any{map[string]any{"param": "Code", "value": "52341"}}, b["inputs"])
	recs := b["receptors"].([]any)
	require.Len(t, recs, 2)
	assert.Equal(t, "09351234567", recs[1].(map[string]any)["mobile"])
	assert.NotEmpty(t, recs[0].(map[string]any)["clientReferenceId"], "required reference is generated")
	assert.Equal(t, []string{"11", "12"}, res.MessageIDs())
}

func TestErrors(t *testing.T) {
	cases := []struct {
		http, status int
		sentinel     error
		retry        bool
	}{
		{200, 401, notijet.ErrUnauthorized, false},
		{200, 412, notijet.ErrInvalidSender, false},
		{200, 418, notijet.ErrInsufficientCredit, false},
		{200, 428, notijet.ErrTemplate, false},
		{429, 429, notijet.ErrTemplate, false},
		{400, 422, nil, false},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprint(tc.http, tc.status), func(t *testing.T) {
			p, _ := server(t, tc.http, func(map[string]any) string {
				return fmt.Sprintf(`{"Data":null,"IsSuccess":false,"StatusCode":%d,"Message":"err"}`, tc.status)
			})
			_, err := p.Send(context.Background(), &notijet.Message{To: []string{"09121234567"}, Text: "x"})
			require.Error(t, err)
			if tc.sentinel != nil {
				assert.ErrorIs(t, err, tc.sentinel)
			}
			assert.Equal(t, fmt.Sprint(tc.status), notijet.ProviderCode(err))
			assert.Equal(t, tc.retry, notijet.IsRetryable(err))
		})
	}

	p, _ := server(t, 429, func(map[string]any) string { return "slow down" })
	_, err := p.Send(context.Background(), &notijet.Message{To: []string{"09121234567"}, Text: "x"})
	assert.ErrorIs(t, err, notijet.ErrRateLimited, "a real rate limit stays one")

	p, _ = server(t, 200, func(map[string]any) string { return `{"IsSuccess":true,"StatusCode":200}` })
	_, err = p.Send(context.Background(), &notijet.Message{To: []string{"09121234567"}, Text: "x"})
	assert.True(t, notijet.IsRetryable(err))
}
