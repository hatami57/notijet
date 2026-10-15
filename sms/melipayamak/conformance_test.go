package melipayamak

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
			return New("user", "s3cr3t-pass", WithBaseURL(baseURL), WithHTTPClient(hc), WithSender("5000"))
		},
		Message: func() *notijet.Message { return &notijet.Message{To: []string{"09121234567"}, Text: "hi"} },
		Secret:  "s3cr3t-pass",
	})
}
