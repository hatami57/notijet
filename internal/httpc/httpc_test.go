package httpc

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/hatami57/microjet/core/errorx"
	"github.com/hatami57/notijet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDoSendsBodyAndHeaders(t *testing.T) {
	var gotCT, gotUA, gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCT, gotUA, gotAuth = r.Header.Get("Content-Type"), r.Header.Get("User-Agent"), r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"bad"}`))
	}))
	defer srv.Close()

	c := New("p", nil)
	req, err := JSON(http.MethodPost, srv.URL, map[string]string{"a": "b"})
	require.NoError(t, err)
	req.BasicAuth("user", "pass").Set("X-Extra", "1")
	resp, err := c.Do(context.Background(), req)
	require.NoError(t, err, "4xx replies are left to the provider")
	assert.Equal(t, http.StatusBadRequest, resp.Status)
	assert.False(t, resp.OK())
	assert.Equal(t, "application/json", gotCT)
	assert.Equal(t, UserAgent, gotUA)
	assert.True(t, strings.HasPrefix(gotAuth, "Basic "))
	assert.JSONEq(t, `{"a":"b"}`, gotBody)

	var out struct{ Error string }
	require.NoError(t, c.Decode(resp, &out))
	assert.Equal(t, "bad", out.Error)
}

func TestFormBody(t *testing.T) {
	var gotCT string
	var form url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCT = r.Header.Get("Content-Type")
		_ = r.ParseForm()
		form = r.PostForm
	}))
	defer srv.Close()

	resp, err := New("p", nil).Do(context.Background(),
		Form(http.MethodPost, srv.URL, url.Values{"to": {"0912"}, "text": {"سلام"}}))
	require.NoError(t, err)
	assert.True(t, resp.OK())
	assert.Equal(t, "application/x-www-form-urlencoded", gotCT)
	assert.Equal(t, "سلام", form.Get("text"))
}

func TestStatusClassification(t *testing.T) {
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte("upstream says no"))
	}))
	defer srv.Close()
	c := New("p", nil)

	status = http.StatusTooManyRequests
	_, err := c.Do(context.Background(), NewRequest(http.MethodGet, srv.URL))
	assert.ErrorIs(t, err, notijet.ErrRateLimited)
	assert.True(t, notijet.IsRetryable(err))

	status = http.StatusBadGateway
	resp, err := c.Do(context.Background(), NewRequest(http.MethodGet, srv.URL))
	require.Error(t, err)
	assert.True(t, errorx.IsInternalError(err))
	assert.Equal(t, "502", notijet.ProviderCode(err))
	assert.Contains(t, err.Error(), "upstream says no")
	assert.Equal(t, 502, resp.Status)

	status = http.StatusOK
	resp, err = c.Do(context.Background(), NewRequest(http.MethodGet, srv.URL))
	require.NoError(t, err)
	var v map[string]any
	err = c.Decode(resp, &v)
	assert.True(t, errorx.IsInternalError(err), "an unreadable 2xx is a fault")
	assert.True(t, errorx.IsInternalError(c.Unexpected(resp, "")))
}

func TestTransportErrorHidesURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
	}))
	defer srv.Close()

	c := New("p", &http.Client{Timeout: 20 * time.Millisecond})
	_, err := c.Do(context.Background(), NewRequest(http.MethodGet, srv.URL+"/v1/SECRET-API-KEY/sms/send.json"))
	require.Error(t, err)
	assert.True(t, notijet.IsRetryable(err))
	assert.NotContains(t, err.Error(), "SECRET-API-KEY")
}

func TestSnippetTruncates(t *testing.T) {
	long := strings.Repeat("x", 2000)
	assert.Less(t, len(Snippet([]byte(long))), 600)
	assert.Equal(t, "short", Snippet([]byte("  short \n")))
}
