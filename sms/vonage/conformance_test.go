package vonage

import (
	"net/http"
	"testing"

	"github.com/hatami57/notijet"
	"github.com/hatami57/notijet/providertest"
)

func TestConformance(t *testing.T) {
	providertest.RunHTTP(t, providertest.HTTPCase{
		Channel: notijet.ChannelSMS,
		New: func(baseURL string, hc *http.Client) notijet.Provider {
			return New("key", "s3cr3t-secret", WithBaseURL(baseURL), WithHTTPClient(hc), WithSender("Acme"))
		},
		Message: func() *notijet.Message { return &notijet.Message{To: []string{"+447700900123"}, Text: "hi"} },
		Secret:  "s3cr3t-secret",
	})
}
