package twilio

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
			return New("AC1", "s3cr3t-token", WithBaseURL(baseURL), WithHTTPClient(hc), WithSender("+15005550006"))
		},
		Message: func() *notijet.Message { return &notijet.Message{To: []string{"+14155550100"}, Text: "hi"} },
		Secret:  "s3cr3t-token",
	})
}
