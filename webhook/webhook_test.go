package webhook

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/hatami57/notijet"
	"github.com/hatami57/notijet/providertest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var fixed = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

type received struct {
	header http.Header
	body   []byte
}

func server(t *testing.T, status int, reply string, opts ...Option) (*Provider, *[]received) {
	t.Helper()
	var got []received
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, received{r.Header.Clone(), b})
		w.WriteHeader(status)
		fmt.Fprint(w, reply)
	}))
	t.Cleanup(srv.Close)
	p := New(srv.URL, opts...)
	p.now = func() time.Time { return fixed }
	return p, &got
}

func full() *notijet.Message {
	return &notijet.Message{
		ID: "m-1", From: "app", To: []string{"ops", " "}, CC: []string{"c"}, BCC: []string{"b"}, ReplyTo: []string{"r"},
		Subject: "Disk full", Text: "db-1 at 95%", HTML: "<b>db-1</b>",
		Template:    notijet.NewTemplate("alert", "host", "db-1"),
		Attachments: []notijet.Attachment{{Filename: "a.txt", ContentType: "text/plain", Content: []byte("hi"), ContentID: "x"}},
		Headers:     map[string]string{"X-A": "1"},
		Data:        map[string]string{"severity": "high"},
		Metadata:    map[string]string{"team": "infra"},
	}
}

func TestSendSignedRoundTrip(t *testing.T) {
	secret := "whsec_1"
	p, got := server(t, 200, `{"id":"evt_9"}`, WithSecret(secret), WithHeader("Authorization", "Bearer abc"))
	res, err := p.Send(context.Background(), full())
	require.NoError(t, err)
	assert.Equal(t, "evt_9", res.MessageID())
	assert.Equal(t, "200", res.Receipts[0].Status)

	r := (*got)[0]
	assert.Equal(t, "application/json", r.header.Get("Content-Type"))
	assert.Equal(t, "Bearer abc", r.header.Get("Authorization"))
	assert.Equal(t, strconv.FormatInt(fixed.Unix(), 10), r.header.Get(TimestampHeader))

	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(r.body))
	req.Header = r.header
	v := Verifier{Secret: []byte(secret), Now: func() time.Time { return fixed.Add(time.Minute) }}
	payload, err := v.Verify(req)
	require.NoError(t, err)
	assert.Equal(t, fixed, payload.SentAt)
	assert.Equal(t, []string{"ops"}, payload.To, "blank recipients are dropped")

	back := payload.Message()
	want := full()
	want.To = []string{"ops"}
	assert.Equal(t, want, back, "the payload carries every message field")
}

func TestVerifierRejects(t *testing.T) {
	body := []byte(`{"text":"x"}`)
	ts := strconv.FormatInt(fixed.Unix(), 10)
	sig := Sign([]byte("s"), ts, body)
	v := Verifier{Secret: []byte("s"), Now: func() time.Time { return fixed }}

	_, err := v.VerifyBody(ts, sig, body)
	require.NoError(t, err)
	_, err = v.VerifyBody(ts, sig, []byte(`{"text":"y"}`))
	assert.ErrorIs(t, err, ErrBadSignature, "tampered body")
	_, err = v.VerifyBody(ts, Sign([]byte("other"), ts, body), body)
	assert.ErrorIs(t, err, ErrBadSignature, "wrong secret")
	_, err = v.VerifyBody("", sig, body)
	assert.ErrorIs(t, err, ErrMissingSignature)
	_, err = v.VerifyBody("abc", sig, body)
	assert.ErrorIs(t, err, ErrStaleTimestamp)

	old := strconv.FormatInt(fixed.Add(-6*time.Minute).Unix(), 10)
	_, err = v.VerifyBody(old, Sign([]byte("s"), old, body), body)
	assert.ErrorIs(t, err, ErrStaleTimestamp, "replayed after the window")
	future := strconv.FormatInt(fixed.Add(6*time.Minute).Unix(), 10)
	_, err = v.VerifyBody(future, Sign([]byte("s"), future, body), body)
	assert.ErrorIs(t, err, ErrStaleTimestamp)
	wide := Verifier{Secret: []byte("s"), Tolerance: 10 * time.Minute, Now: v.Now}
	_, err = wide.VerifyBody(old, Sign([]byte("s"), old, body), body)
	assert.NoError(t, err)

	_, err = Verifier{}.VerifyBody(ts, Sign(nil, ts, body), body)
	assert.ErrorIs(t, err, ErrNoSecret, "an empty secret verifies nothing")

	notJSON := []byte("nope")
	_, err = v.VerifyBody(ts, Sign([]byte("s"), ts, notJSON), notJSON)
	assert.Error(t, err)

	_, err = v.Verify(httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body)))
	assert.ErrorIs(t, err, ErrMissingSignature)
	_, err = v.Verify(nil)
	assert.ErrorIs(t, err, ErrInvalidRequest)
	_, err = v.Verify(&http.Request{Header: http.Header{TimestampHeader: {ts}, SignatureHeader: {sig}}})
	assert.ErrorIs(t, err, ErrInvalidRequest)
	_, err = Verifier{Secret: []byte("s")}.VerifyBody(strconv.FormatInt(time.Now().Unix(), 10), "sha256=00", body)
	assert.ErrorIs(t, err, ErrBadSignature, "the real clock is used by default")
}

func TestUnsignedAndReplies(t *testing.T) {
	p, got := server(t, 204, "", WithChannel(notijet.ChannelChat))
	assert.Equal(t, notijet.ChannelChat, p.Channel())
	res, err := p.Send(context.Background(), &notijet.Message{Data: map[string]string{"k": "v"}})
	require.NoError(t, err, "data-only messages and no recipients are fine")
	assert.Empty(t, (*got)[0].header.Get(SignatureHeader))
	assert.Empty(t, res.MessageID())

	for reply, want := range map[string]string{`{"id":42}`: "42", `not json`: "", `{"other":1}`: ""} {
		p, _ := server(t, 200, reply)
		res, err := p.Send(context.Background(), &notijet.Message{Text: "x"})
		require.NoError(t, err)
		assert.Equal(t, want, res.MessageID(), reply)
	}
}

func TestErrors(t *testing.T) {
	cases := []struct {
		status   int
		sentinel error
		retry    bool
	}{
		{401, notijet.ErrUnauthorized, false},
		{403, notijet.ErrUnauthorized, false},
		{404, nil, false},
		{408, nil, true},
		{422, nil, false},
	}
	for _, tc := range cases {
		t.Run(strconv.Itoa(tc.status), func(t *testing.T) {
			p, _ := server(t, tc.status, "nope")
			_, err := p.Send(context.Background(), &notijet.Message{Text: "x"})
			require.Error(t, err)
			if tc.sentinel != nil {
				assert.ErrorIs(t, err, tc.sentinel)
			}
			assert.Equal(t, strconv.Itoa(tc.status), notijet.ProviderCode(err))
			assert.Equal(t, tc.retry, notijet.IsRetryable(err))
			assert.Contains(t, err.Error(), "nope")
		})
	}
}

func TestValidation(t *testing.T) {
	p, got := server(t, 200, "")
	for name, msg := range map[string]*notijet.Message{
		"nil":         nil,
		"empty":       {To: []string{"x"}},
		"template id": {Template: &notijet.Template{}},
	} {
		_, err := p.Send(context.Background(), msg)
		assert.Error(t, err, name)
	}
	_, err := New("").Send(context.Background(), &notijet.Message{Text: "x"})
	assert.Error(t, err)
	assert.Empty(t, *got)
	assert.Equal(t, "webhook", p.Name())
}

func TestConformance(t *testing.T) {
	providertest.RunHTTP(t, providertest.HTTPCase{
		Channel: notijet.ChannelWebhook,
		New: func(baseURL string, hc *http.Client) notijet.Provider {
			return New(baseURL+"/hook", WithHTTPClient(hc), WithSecret("s3cr3t"), WithHeader("Authorization", "Bearer s3cr3t"))
		},
		Message:           func() *notijet.Message { return &notijet.Message{To: []string{"ops"}, Text: "hi"} },
		Secret:            "s3cr3t",
		RecipientOptional: true,
		AnySuccessBody:    true,
	})
}
