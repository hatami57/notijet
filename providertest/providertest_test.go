package providertest_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/hatami57/notijet"
	"github.com/hatami57/notijet/providertest"
)

// echo is a minimal provider written the way the suite expects: it validates
// first and classifies HTTP failures with notijet's error helpers.
type echo struct {
	url string
	hc  *http.Client
}

func (e *echo) Name() string             { return "echo" }
func (e *echo) Channel() notijet.Channel { return notijet.ChannelWebhook }

func (e *echo) Send(ctx context.Context, msg *notijet.Message) (*notijet.Result, error) {
	if err := notijet.Check("echo", notijet.ChannelWebhook, msg); err != nil {
		return nil, err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, e.url, nil)
	hc := e.hc
	if hc == nil {
		hc = notijet.DefaultHTTPClient()
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, notijet.Fault("echo", "request failed", ctx.Err())
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, notijet.Throttled("echo", "429", "")
	case resp.StatusCode != http.StatusNoContent:
		return nil, notijet.Fault("echo", "unexpected reply", nil)
	}
	return &notijet.Result{Provider: "echo"}, nil
}

func TestRunHTTP(t *testing.T) {
	providertest.RunHTTP(t, providertest.HTTPCase{
		Channel: notijet.ChannelWebhook,
		New:     func(baseURL string, hc *http.Client) notijet.Provider { return &echo{url: baseURL, hc: hc} },
		Message: func() *notijet.Message { return &notijet.Message{To: []string{"x"}, Text: "hi"} },
		Secret:  "never-sent",
	})
}
