package melipayamak

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
	path string
	form url.Values
}

func server(t *testing.T, reply string) (*Provider, *[]call) {
	t.Helper()
	var calls []call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		calls = append(calls, call{r.URL.Path, r.PostForm})
		fmt.Fprint(w, reply)
	}))
	t.Cleanup(srv.Close)
	return New("user", "pass", WithBaseURL(srv.URL+"/api/SendSMS"), WithSender("50004001")), &calls
}

func TestSendSMS(t *testing.T) {
	p, calls := server(t, `{"Value":"2051345678912345678","RetStatus":1,"StrRetStatus":"Ok"}`)
	res, err := p.Send(context.Background(), &notijet.Message{
		To: []string{"09121234567", "09351234567"}, Text: "hi", Data: map[string]string{"flash": "yes"},
	})
	require.NoError(t, err)
	c := (*calls)[0]
	assert.Equal(t, "/api/SendSMS/SendSMS", c.path)
	assert.Equal(t, "user", c.form.Get("username"))
	assert.Equal(t, "pass", c.form.Get("password"))
	assert.Equal(t, "09121234567,09351234567", c.form.Get("to"))
	assert.Equal(t, "50004001", c.form.Get("from"))
	assert.Equal(t, "hi", c.form.Get("text"))
	assert.Equal(t, "true", c.form.Get("isFlash"))
	assert.Equal(t, "2051345678912345678", res.MessageID())
	assert.Len(t, res.Receipts, 1, "one recId for the request")
}

func TestPerRecipientIDs(t *testing.T) {
	p, _ := server(t, `{"Value":"1000001,1000002","RetStatus":1,"StrRetStatus":"Ok"}`)
	res, err := p.Send(context.Background(), &notijet.Message{To: []string{"09121234567", "09351234567"}, Text: "hi"})
	require.NoError(t, err)
	assert.Equal(t, []string{"1000001", "1000002"}, res.MessageIDs())
	assert.Equal(t, "09351234567", res.Receipts[1].To)
}

func TestBaseServiceNumber(t *testing.T) {
	p, calls := server(t, `{"Value":"4567891234","RetStatus":1,"StrRetStatus":"Ok"}`)
	res, err := p.Send(context.Background(), &notijet.Message{
		To:       []string{"09121234567", "09351234567"},
		Template: notijet.NewTemplate("12345", "code", "111", "name", "Ali"),
	})
	require.NoError(t, err)
	require.Len(t, *calls, 2)
	c := (*calls)[1]
	assert.Equal(t, "/api/SendSMS/BaseServiceNumber", c.path)
	assert.Equal(t, "09351234567", c.form.Get("to"))
	assert.Equal(t, "12345", c.form.Get("bodyId"))
	assert.Equal(t, "111;Ali", c.form.Get("text"))
	assert.Len(t, res.Receipts, 2)

	_, err = p.Send(context.Background(), &notijet.Message{To: []string{"09121234567"}, Template: notijet.NewTemplate("otp")})
	assert.ErrorIs(t, err, notijet.ErrTemplate)
}

func TestErrors(t *testing.T) {
	cases := []struct {
		reply    string
		code     string
		sentinel error
		retry    bool
	}{
		{`{"Value":"0","RetStatus":0,"StrRetStatus":"InvalidUserPass"}`, "0", notijet.ErrUnauthorized, false},
		{`{"Value":"2","RetStatus":2,"StrRetStatus":"NoCredit"}`, "2", notijet.ErrInsufficientCredit, false},
		{`{"Value":"5","RetStatus":5,"StrRetStatus":"InvalidFrom"}`, "5", notijet.ErrInvalidSender, false},
		{`{"Value":"-4","RetStatus":1,"StrRetStatus":"Ok"}`, "-4", notijet.ErrTemplate, false},
		{`{"Value":"6","RetStatus":6,"StrRetStatus":"Updating"}`, "6", nil, true},
		{`{"Value":"7","RetStatus":7,"StrRetStatus":"Filtered"}`, "7", nil, false},
		{`{"Value":"1234567","RetStatus":3,"StrRetStatus":"x"}`, "3", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			p, _ := server(t, tc.reply)
			_, err := p.Send(context.Background(), &notijet.Message{To: []string{"09121234567"}, Text: "x"})
			require.Error(t, err)
			if tc.sentinel != nil {
				assert.ErrorIs(t, err, tc.sentinel)
			}
			assert.Equal(t, tc.code, notijet.ProviderCode(err))
			assert.Equal(t, tc.retry, notijet.IsRetryable(err))
		})
	}

	p, _ := server(t, `{"Value":"oops","RetStatus":1}`)
	_, err := p.Send(context.Background(), &notijet.Message{To: []string{"09121234567"}, Text: "x"})
	assert.True(t, notijet.IsRetryable(err))

	_, err = New("u", "p").Send(context.Background(), &notijet.Message{To: []string{"09121234567"}, Text: "x"})
	assert.ErrorIs(t, err, notijet.ErrInvalidSender)
	_, err = New("", "").Send(context.Background(), &notijet.Message{To: []string{"09121234567"}, Text: "x"})
	assert.Error(t, err)
}
