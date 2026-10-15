// Failover: an OTP sent as a Kavenegar lookup template, retried on
// transient errors, falling over to Twilio with plain text when Kavenegar
// refuses it. Both APIs are simulated by local test servers, so it runs
// without credentials:
//
//	go run ./examples/failover
//
// The message carries both a Template (for providers with server-side
// templates) and Text (for the rest), so one Message works across the chain.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/hatami57/notijet"
	"github.com/hatami57/notijet/sms/kavenegar"
	"github.com/hatami57/notijet/sms/twilio"
)

func main() {
	// Kavenegar answers "insufficient credit" (status 418): a permanent
	// refusal, so Retry gives up at once and Failover moves on.
	kavenegarAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(418)
		fmt.Fprint(w, `{"return":{"status":418,"message":"اعتبار کافی نیست"},"entries":null}`)
	}))
	defer kavenegarAPI.Close()

	// Twilio fails once with a 503, then accepts: Retry absorbs the blip.
	calls := 0
	twilioAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"sid":"SM0001","status":"queued","price":null,"price_unit":"USD"}`)
	}))
	defer twilioAPI.Close()

	retry := []notijet.RetryOption{notijet.RetryAttempts(3), notijet.RetryBackoff(50*time.Millisecond, time.Second)}
	otp := notijet.Failover(
		notijet.Retry(kavenegar.New("demo-key", kavenegar.WithBaseURL(kavenegarAPI.URL)), retry...),
		notijet.Retry(twilio.New("AC-demo", "demo-token", twilio.WithBaseURL(twilioAPI.URL),
			twilio.WithSender("+15005550006"), twilio.WithDefaultCountry("98")), retry...),
	)

	res, err := otp.Send(context.Background(), &notijet.Message{
		To:       []string{"09121234567"},
		Template: notijet.NewTemplate("verify", "token", "52341"), // Kavenegar lookup
		Text:     "Your verification code is 52341",               // everyone else
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("delivered by %s: %s (%s)\n", res.Provider, res.MessageID(), res.Receipts[0].To)
	fmt.Println("twilio calls (one 503, one success):", calls)

	// Provider errors say what went wrong in a provider-neutral way: the
	// sentinel for branching, the provider's own code for the logs, and
	// whether sending again could help.
	_, err = kavenegar.New("demo-key", kavenegar.WithBaseURL(kavenegarAPI.URL)).
		Send(context.Background(), &notijet.Message{To: []string{"09121234567"}, Text: "hi"})
	fmt.Println("no credit:", errors.Is(err, notijet.ErrInsufficientCredit),
		"code:", notijet.ProviderCode(err), "retryable:", notijet.IsRetryable(err))
}
