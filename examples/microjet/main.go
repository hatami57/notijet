// Microjet host: notijet as a microjet module — providers from config.toml,
// a queue in the app's database and its worker, all started and stopped with
// the app. Run it from this directory (the config is read from the working
// directory) and stop it with Ctrl-C:
//
//	cd examples/microjet && go run .
package main

import (
	"context"

	"github.com/hatami57/microjet/gormx"
	"github.com/hatami57/microjet/gormx/sqlite"
	"github.com/hatami57/microjet/host"
	"github.com/hatami57/notijet"
	"github.com/hatami57/notijet/config/builtin"
	"github.com/hatami57/notijet/module"
	"github.com/hatami57/notijet/queue"
)

func main() {
	app := host.MustNew().WithModules(
		gormx.Module(sqlite.Driver()),
		module.New(
			module.WithConfig(builtin.Registry()),
			module.WithQueue(queue.MaxAttempts(5)),
		),
	)

	// Anything in the app can now send right away through the Notifier, or
	// enqueue — inside its own database transaction — through the queue.
	app.WithWorker("welcome", func(ctx context.Context, app *host.App) error {
		_, err := module.Notifier(app).Send(ctx, notijet.ChannelSMS, &notijet.Message{
			To:   []string{"09121234567"},
			Text: "Sent immediately through the Notifier.",
		})
		if err != nil {
			return err
		}
		_, err = module.Queue(app).Enqueue(ctx, notijet.ChannelEmail, &notijet.Message{
			ID:      "welcome-ali", // enqueueing the same ID again is a no-op
			From:    "Acme <no-reply@acme.example>",
			To:      []string{"ali@example.com"},
			Subject: "Welcome to Acme",
			Text:    "Delivered by the queue worker.",
		})
		return err
	})
	app.MustRun()
}
