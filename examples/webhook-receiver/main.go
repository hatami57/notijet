// Webhook receiver: one side sends notifications to an HTTP endpoint with the
// webhook provider, signed with a shared secret; the other side verifies the
// signature and the timestamp before trusting the payload. Both run in this
// process on a local port:
//
//	go run ./examples/webhook-receiver
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"

	"github.com/hatami57/notijet"
	"github.com/hatami57/notijet/webhook"
)

const secret = "whsec_example"

func main() {
	// Receiver: reject anything not signed with the secret in the last 5 min.
	v := webhook.Verifier{Secret: []byte(secret)}
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload, err := v.Verify(r)
		if err != nil {
			http.Error(w, "forbidden", http.StatusForbidden)
			fmt.Println("receiver: rejected:", err)
			return
		}
		fmt.Printf("receiver: %q to %v (data %v)\n", payload.Subject, payload.To, payload.Data)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"evt_1"}`)
	}))
	defer receiver.Close()

	// Sender.
	p := webhook.New(receiver.URL, webhook.WithSecret(secret))
	res, err := p.Send(context.Background(), &notijet.Message{
		To:      []string{"ops-team"},
		Subject: "Disk almost full",
		Text:    "db-1 is at 95%",
		Data:    map[string]string{"host": "db-1", "severity": "high"},
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("sender: accepted with id", res.MessageID())

	// A sender without the secret is turned away.
	_, err = webhook.New(receiver.URL, webhook.WithSecret("wrong")).Send(context.Background(),
		&notijet.Message{Text: "forged"})
	fmt.Println("sender: forged request refused:", err != nil, "retryable:", notijet.IsRetryable(err))
}
