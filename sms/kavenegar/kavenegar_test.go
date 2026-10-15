package kavenegar

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/hatami57/microjet/core/errorx"
	"github.com/hatami57/notijet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type call struct {
	path string
	form url.Values
}

// server answers every request with status and body, recording the calls.
func server(t *testing.T, status int, body func(form url.Values) string) (*Provider, *[]call) {
	t.Helper()
	var mu sync.Mutex
	var calls []call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		mu.Lock()
		calls = append(calls, call{r.URL.EscapedPath(), r.PostForm})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		fmt.Fprint(w, body(r.PostForm))
	}))
	t.Cleanup(srv.Close)
	return New("key/1", WithBaseURL(srv.URL+"/v1/"), WithSender("10004346")), &calls
}

func okReply(form url.Values) string {
	var entries []string
	for i, to := range strings.Split(form.Get("receptor"), ",") {
		entries = append(entries, fmt.Sprintf(`{"messageid":%d,"status":1,"statustext":"در صف ارسال","receptor":"%s","cost":120}`, 1000+i, to))
	}
	return `{"return":{"status":200,"message":"تایید شد"},"entries":[` + strings.Join(entries, ",") + `]}`
}

func TestSendText(t *testing.T) {
	p, calls := server(t, 200, okReply)
	res, err := p.Send(context.Background(), &notijet.Message{
		ID:   "42",
		To:   []string{"+98 912 123 4567", "۰۹۳۵۱۲۳۴۵۶۷", "09121234567"},
		Text: "hello",
	})
	require.NoError(t, err)
	require.Len(t, *calls, 1)
	c := (*calls)[0]
	assert.Equal(t, "/v1/key%2F1/sms/send.json", c.path, "API key is path-escaped")
	assert.Equal(t, "09121234567,09351234567", c.form.Get("receptor"), "normalized and deduplicated")
	assert.Equal(t, "hello", c.form.Get("message"))
	assert.Equal(t, "10004346", c.form.Get("sender"))
	assert.Empty(t, c.form.Get("localid"), "localid only for a single recipient")

	assert.Equal(t, "kavenegar", res.Provider)
	assert.Equal(t, []string{"1000", "1001"}, res.MessageIDs())
	assert.Equal(t, "09351234567", res.Receipts[1].To)
	assert.Equal(t, "120", res.Receipts[0].Cost)
}

func TestSendTextLocalIDAndFrom(t *testing.T) {
	p, calls := server(t, 200, okReply)
	_, err := p.Send(context.Background(), &notijet.Message{ID: "42", To: []string{"09121234567"}, From: "2000", Text: "x"})
	require.NoError(t, err)
	assert.Equal(t, "42", (*calls)[0].form.Get("localid"))
	assert.Equal(t, "2000", (*calls)[0].form.Get("sender"))
}

func TestSendTextBatches(t *testing.T) {
	p, calls := server(t, 200, okReply)
	var to []string
	for i := range 450 {
		to = append(to, fmt.Sprintf("0912%07d", i))
	}
	res, err := p.Send(context.Background(), &notijet.Message{To: to, Text: "x"})
	require.NoError(t, err)
	assert.Len(t, *calls, 3)
	assert.Len(t, res.Receipts, 450)
}

func TestLookupByNameAndPosition(t *testing.T) {
	p, calls := server(t, 200, okReply)
	_, err := p.Send(context.Background(), &notijet.Message{
		To:       []string{"09121234567", "09351234567"},
		Template: notijet.NewTemplate("verify", "token", "123", "token10", "Ali Reza"),
		Data:     map[string]string{"type": "call"},
	})
	require.NoError(t, err)
	require.Len(t, *calls, 2, "one lookup per recipient")
	f := (*calls)[1].form
	assert.True(t, strings.HasSuffix((*calls)[1].path, "/verify/lookup.json"))
	assert.Equal(t, "09351234567", f.Get("receptor"))
	assert.Equal(t, "verify", f.Get("template"))
	assert.Equal(t, "123", f.Get("token"))
	assert.Equal(t, "Ali Reza", f.Get("token10"))
	assert.Equal(t, "call", f.Get("type"))

	_, err = p.Send(context.Background(), &notijet.Message{
		To:       []string{"09121234567"},
		Template: notijet.NewTemplate("order", "code", "1", "name", "x", "date", "y"),
	})
	require.NoError(t, err)
	f = (*calls)[2].form
	assert.Equal(t, []string{"1", "x", "y"}, []string{f.Get("token"), f.Get("token2"), f.Get("token3")})

	_, err = p.Send(context.Background(), &notijet.Message{
		To:       []string{"09121234567"},
		Template: notijet.NewTemplate("t", "a", "1", "b", "2", "c", "3", "d", "4", "e", "5", "f", "6"),
	})
	assert.ErrorIs(t, err, notijet.ErrTemplate)
}

func TestErrors(t *testing.T) {
	cases := []struct {
		status   int
		sentinel error
		retry    bool
	}{
		{403, notijet.ErrUnauthorized, false},
		{411, notijet.ErrInvalidRecipient, false},
		{412, notijet.ErrInvalidSender, false},
		{418, notijet.ErrInsufficientCredit, false},
		{424, notijet.ErrTemplate, false},
		{451, notijet.ErrRateLimited, true},
		{409, nil, true},
		{414, nil, false},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			p, _ := server(t, tc.status, func(url.Values) string {
				return fmt.Sprintf(`{"return":{"status":%d,"message":"err"},"entries":null}`, tc.status)
			})
			_, err := p.Send(context.Background(), &notijet.Message{To: []string{"09121234567"}, Text: "x"})
			require.Error(t, err)
			if tc.sentinel != nil {
				assert.ErrorIs(t, err, tc.sentinel)
			}
			assert.Equal(t, tc.retry, notijet.IsRetryable(err))
			assert.Equal(t, fmt.Sprint(tc.status), notijet.ProviderCode(err))
			assert.Equal(t, "kavenegar", errorx.GetError(err).Subject)
		})
	}
}

func TestPartialFailure(t *testing.T) {
	n := 0
	p, _ := server(t, 200, func(form url.Values) string {
		n++
		if n == 2 {
			return `{"return":{"status":418,"message":"credit"},"entries":null}`
		}
		return okReply(form)
	})
	_, err := p.Send(context.Background(), &notijet.Message{
		To:       []string{"09121234567", "09351234567"},
		Template: notijet.NewTemplate("verify", "token", "1"),
	})
	var pe *notijet.PartialError
	require.ErrorAs(t, err, &pe)
	assert.Len(t, pe.Result.Receipts, 1)
	assert.ErrorIs(t, err, notijet.ErrInsufficientCredit)
	assert.False(t, notijet.IsRetryable(err))
}

func TestValidationBeforeNetwork(t *testing.T) {
	p, calls := server(t, 200, okReply)
	_, err := p.Send(context.Background(), &notijet.Message{To: []string{"12"}, Text: "x"})
	assert.ErrorIs(t, err, notijet.ErrInvalidRecipient)
	_, err = p.Send(context.Background(), &notijet.Message{To: []string{"09121234567"}, HTML: "<b>x</b>"})
	assert.ErrorIs(t, err, notijet.ErrUnsupported)
	_, err = New("").Send(context.Background(), &notijet.Message{To: []string{"09121234567"}, Text: "x"})
	assert.True(t, errorx.IsBadRequestError(err))
	assert.Empty(t, *calls)
}

func TestUnreadableAndServerErrors(t *testing.T) {
	p, _ := server(t, 200, func(url.Values) string { return "<html>" })
	_, err := p.Send(context.Background(), &notijet.Message{To: []string{"09121234567"}, Text: "x"})
	assert.True(t, errorx.IsInternalError(err))

	p, _ = server(t, 502, func(url.Values) string { return "bad gateway" })
	_, err = p.Send(context.Background(), &notijet.Message{To: []string{"09121234567"}, Text: "x"})
	assert.True(t, notijet.IsRetryable(err))
	assert.NotContains(t, err.Error(), "key", "API key must not leak into errors")
}

func TestOKWithoutEntries(t *testing.T) {
	p, _ := server(t, 200, func(url.Values) string { return `{"return":{"status":200,"message":"ok"},"entries":[]}` })
	_, err := p.Send(context.Background(), &notijet.Message{To: []string{"09121234567"}, Text: "x"})
	assert.True(t, notijet.IsRetryable(err), "a success without entries is unexpected")
}
