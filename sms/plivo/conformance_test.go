package plivo

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
			return New("MA1", "s3cr3t-token", WithBaseURL(baseURL), WithHTTPClient(hc), WithSender("+14155550100"))
		},
		Message: func() *notijet.Message { return &notijet.Message{To: []string{"+447700900123"}, Text: "hi"} },
		Secret:  "s3cr3t-token",
	})
}
