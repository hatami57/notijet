package twilio

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/hatami57/notijet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type call struct {
	path, user, pass string
	form             url.Values
}

func server(t *testing.T, status int, reply string, opts ...Option) (*Provider, *[]call) {
	t.Helper()
	var calls []call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		u, pw, _ := r.BasicAuth()
		calls = append(calls, call{r.URL.Path, u, pw, r.PostForm})
		w.WriteHeader(status)
		fmt.Fprint(w, reply)
	}))
	t.Cleanup(srv.Close)
	opts = append([]Option{WithBaseURL(srv.URL), WithSender("+15005550006")}, opts...)
	return New("AC123", "token", opts...), &calls
}

const queued = `{"sid":"SM1","status":"queued","price":null,"price_unit":"USD"}`

func TestSend(t *testing.T) {
	p, calls := server(t, 201, queued, WithDefaultCountry("1"), WithStatusCallback("https://x.io/cb"))
	res, err := p.Send(context.Background(), &notijet.Message{To: []string{"+1 415 555 0100", "04155550101"}, Text: "hi"})
	require.NoError(t, err)
	require.Len(t, *calls, 2)
	c := (*calls)[1]
	assert.Equal(t, "/Accounts/AC123/Messages.json", c.path)
	assert.Equal(t, "AC123", c.user)
	assert.Equal(t, "token", c.pass)
	assert.Equal(t, "+14155550101", c.form.Get("To"))
	assert.Equal(t, "+15005550006", c.form.Get("From"))
	assert.Equal(t, "hi", c.form.Get("Body"))
	assert.Equal(t, "https://x.io/cb", c.form.Get("StatusCallback"))
	assert.Equal(t, []string{"SM1", "SM1"}, res.MessageIDs())
	assert.Equal(t, "queued", res.Receipts[0].Status)
}

func TestMessagingServiceAndAPIKey(t *testing.T) {
	p, calls := server(t, 201, `{"sid":"SM2","status":"accepted","price":"-0.0075","price_unit":"USD"}`, WithAPIKey("SK1", "secret"))
	res, err := p.Send(context.Background(), &notijet.Message{To: []string{"+14155550100"}, From: "MG999", Text: "hi"})
	require.NoError(t, err)
	c := (*calls)[0]
	assert.Equal(t, "MG999", c.form.Get("MessagingServiceSid"))
	assert.Empty(t, c.form.Get("From"))
	assert.Equal(t, "SK1", c.user)
	assert.Equal(t, "-0.0075 USD", res.Receipts[0].Cost)
}

func TestTemplateNeedsText(t *testing.T) {
	p, calls := server(t, 201, queued)
	_, err := p.Send(context.Background(), &notijet.Message{To: []string{"+14155550100"}, Template: notijet.NewTemplate("t")})
	assert.ErrorIs(t, err, notijet.ErrUnsupported)
	_, err = p.Send(context.Background(), &notijet.Message{To: []string{"+14155550100"}, Template: notijet.NewTemplate("t"), Text: "fallback"})
	require.NoError(t, err)
	assert.Equal(t, "fallback", (*calls)[0].form.Get("Body"))
	_, err = p.Send(context.Background(), &notijet.Message{To: []string{"04155550100"}, Text: "x"})
	assert.ErrorIs(t, err, notijet.ErrInvalidRecipient, "national form needs WithDefaultCountry")
}

func TestErrors(t *testing.T) {
	cases := []struct {
		status   int
		body     string
		code     string
		sentinel error
		retry    bool
	}{
		{401, `{"code":20003,"message":"Authenticate","status":401}`, "20003", notijet.ErrUnauthorized, false},
		{400, `{"code":21211,"message":"Invalid 'To'","status":400}`, "21211", notijet.ErrInvalidRecipient, false},
		{400, `{"code":21606,"message":"bad From","status":400}`, "21606", notijet.ErrInvalidSender, false},
		{400, `{"code":21617,"message":"too long","status":400}`, "21617", nil, false},
		{429, `{"code":20429,"message":"Too Many Requests"}`, "429", notijet.ErrRateLimited, true},
		{401, `unauthorized`, "401", notijet.ErrUnauthorized, false},
		{201, `{"sid":"SM3","status":"failed","error_code":21610,"error_message":"unsubscribed"}`, "21610", notijet.ErrInvalidRecipient, false},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			p, _ := server(t, tc.status, tc.body)
			_, err := p.Send(context.Background(), &notijet.Message{To: []string{"+14155550100"}, Text: "x"})
			require.Error(t, err)
			if tc.sentinel != nil {
				assert.ErrorIs(t, err, tc.sentinel)
			}
			assert.Equal(t, tc.code, notijet.ProviderCode(err))
			assert.Equal(t, tc.retry, notijet.IsRetryable(err))
		})
	}
}
