// Quickstart: a Notifier with one provider per channel, sending by channel
// and by name. It uses the logger provider, which prints messages instead of
// delivering them, so it runs without any credentials:
//
//	go run ./examples/quickstart
//
// Swap logger.New for a real provider — kavenegar.New(apiKey), smtp.Gmail.New(...)
// — and nothing else changes.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"os"

	"github.com/hatami57/notijet"
	"github.com/hatami57/notijet/logger"
)

func main() {
	out := slog.New(slog.NewTextHandler(os.Stdout, nil))

	n := notijet.NewNotifier(notijet.WithLogger(out))
	n.MustRegister("sms", logger.New(out, logger.WithName("dev-sms")))
	n.MustRegister("mail", logger.New(out, logger.WithName("dev-mail"), logger.WithChannel(notijet.ChannelEmail)))

	ctx := context.Background()

	// By channel: the channel's default provider sends it.
	res, err := n.Send(ctx, notijet.ChannelSMS, &notijet.Message{
		To:   []string{"09121234567"},
		Text: "Your order #1042 has shipped.",
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("sms sent via", res.Provider, "id", res.MessageID())

	// By name.
	res, err = n.SendVia(ctx, "mail", &notijet.Message{
		From:    "Acme <no-reply@acme.example>",
		To:      []string{"customer@example.com"},
		Subject: "Order #1042 shipped",
		Text:    "Your order is on its way.",
		HTML:    "<p>Your order is <b>on its way</b>.</p>",
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("email sent via", res.Provider, "id", res.MessageID())

	// Errors carry a category and a sentinel: an email without a subject is
	// rejected before anything is sent, and retrying would not help.
	_, err = n.SendVia(ctx, "mail", &notijet.Message{To: []string{"customer@example.com"}, Text: "no subject"})
	fmt.Println("invalid email:", err != nil, "retryable:", notijet.IsRetryable(err))
	_, err = n.Send(ctx, notijet.ChannelPush, &notijet.Message{To: []string{"device"}, Text: "x"})
	fmt.Println("no push provider:", errors.Is(err, notijet.ErrNoProvider))
}
