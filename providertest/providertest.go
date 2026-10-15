// Package providertest is a conformance suite for notijet providers that talk
// HTTP. It checks the behaviour every provider must share, whatever its API:
// validation before any request, the error categories of transport failures,
// 5xx, 429 and unreadable replies, use of the configured *http.Client,
// cancellation, and that credentials never leak into error text.
//
//	func TestConformance(t *testing.T) {
//	    providertest.RunHTTP(t, providertest.HTTPCase{
//	        Channel: notijet.ChannelSMS,
//	        New: func(baseURL string, hc *http.Client) notijet.Provider {
//	            return mysms.New("secret-key", mysms.WithBaseURL(baseURL), mysms.WithHTTPClient(hc))
//	        },
//	        Message: func() *notijet.Message { return &notijet.Message{To: []string{"+14155550100"}, Text: "hi"} },
//	        Secret:  "secret-key",
//	    })
//	}
package providertest

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hatami57/microjet/core/errorx"
	"github.com/hatami57/notijet"
)

// HTTPCase describes the provider under test.
type HTTPCase struct {
	// Channel is the channel the provider must report.
	Channel notijet.Channel
	// New builds the provider against baseURL using hc for every request.
	New func(baseURL string, hc *http.Client) notijet.Provider
	// Message returns a fresh message the provider accepts.
	Message func() *notijet.Message
	// Secret is a credential passed to New that must never appear in an error.
	Secret string
}

// RunHTTP runs the conformance checks as subtests of t.
func RunHTTP(t *testing.T, c HTTPCase) {
	t.Helper()

	t.Run("identity", func(t *testing.T) {
		p := c.New("http://127.0.0.1:1", nil)
		if p.Name() == "" {
			t.Error("Name is empty")
		}
		if p.Channel() != c.Channel {
			t.Errorf("Channel = %q, want %q", p.Channel(), c.Channel)
		}
	})

	t.Run("validates before any request", func(t *testing.T) {
		var hits atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
		defer srv.Close()
		p := c.New(srv.URL, srv.Client())

		invalid := map[string]*notijet.Message{"nil message": nil}
		m := c.Message()
		m.To, m.CC, m.BCC = nil, nil, nil
		invalid["no recipient"] = m
		m = c.Message()
		m.Text, m.HTML, m.Template = "", "", nil
		invalid["no body"] = m
		for name, msg := range invalid {
			_, err := p.Send(context.Background(), msg)
			if !errorx.IsBadRequestError(err) {
				t.Errorf("%s: got %v, want a BadRequest error", name, err)
			}
			checkSubject(t, name, err, p.Name())
		}
		if n := hits.Load(); n != 0 {
			t.Errorf("invalid messages made %d requests", n)
		}
	})

	failures := []struct {
		name   string
		status int
		body   string
		check  func(error) bool
		want   string
	}{
		{"5xx is a retryable fault", http.StatusBadGateway, "bad gateway", errorx.IsInternalError, "an Internal error"},
		{"429 is a rate limit", http.StatusTooManyRequests, "slow down", func(err error) bool {
			return errors.Is(err, notijet.ErrRateLimited)
		}, "ErrRateLimited"},
		{"unreadable 200 is a fault", http.StatusOK, "<html>not json</html>", errorx.IsInternalError, "an Internal error"},
	}
	for _, f := range failures {
		t.Run(f.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(f.status)
				_, _ = w.Write([]byte(f.body))
			}))
			defer srv.Close()
			p := c.New(srv.URL, srv.Client())
			_, err := p.Send(context.Background(), c.Message())
			if err == nil || !f.check(err) {
				t.Fatalf("got %v, want %s", err, f.want)
			}
			if !notijet.IsRetryable(err) {
				t.Errorf("%v is not retryable", err)
			}
			checkSubject(t, f.name, err, p.Name())
			checkSecret(t, err, c.Secret)
		})
	}

	t.Run("transport failure is a retryable fault", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		url := srv.URL
		srv.Close()
		p := c.New(url, nil)
		_, err := p.Send(context.Background(), c.Message())
		if !errorx.IsInternalError(err) || !notijet.IsRetryable(err) {
			t.Fatalf("got %v, want a retryable Internal error", err)
		}
		checkSecret(t, err, c.Secret)
	})

	t.Run("uses the configured HTTP client", func(t *testing.T) {
		var used atomic.Int32
		hc := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			used.Add(1)
			return nil, errors.New("stub transport")
		})}
		p := c.New("http://provider.invalid", hc)
		if _, err := p.Send(context.Background(), c.Message()); err == nil {
			t.Fatal("expected the stub transport's error")
		}
		if used.Load() == 0 {
			t.Error("the custom HTTP client was not used")
		}
	})

	t.Run("cancelled context is not retryable", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		defer srv.Close()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := c.New(srv.URL, srv.Client()).Send(ctx, c.Message())
		if err == nil {
			t.Fatal("expected an error")
		}
		if notijet.IsRetryable(err) {
			t.Errorf("%v is retryable after the caller cancelled", err)
		}
	})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func checkSubject(t *testing.T, name string, err error, want string) {
	t.Helper()
	if e := errorx.GetError(err); e != nil && e.Subject != want {
		t.Errorf("%s: error subject = %q, want %q", name, e.Subject, want)
	}
}

func checkSecret(t *testing.T, err error, secret string) {
	t.Helper()
	if secret != "" && err != nil && strings.Contains(err.Error(), secret) {
		t.Errorf("error leaks the secret: %v", err)
	}
}
