// Queue: notifications stored in the same database transaction as the order
// they announce, then delivered by a worker that retries transient failures.
// It uses an in-memory SQLite database and a memory provider that fails the
// first delivery, so it runs without credentials:
//
//	go run ./examples/queue
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"time"

	"github.com/hatami57/microjet/gormx"
	"github.com/hatami57/microjet/gormx/sqlite"
	"github.com/hatami57/notijet"
	"github.com/hatami57/notijet/memory"
	"github.com/hatami57/notijet/queue"
)

// Order is the domain record the notification announces.
type Order struct {
	ID    string `gorm:"primaryKey"`
	Phone string
}

func main() {
	ctx := context.Background()
	quiet := slog.New(slog.DiscardHandler)
	db, err := sqlite.Driver().Open(gormx.Config{Name: ":memory:"}, quiet)
	if err != nil {
		log.Fatal(err)
	}
	if err := db.AutoMigrate(&Order{}); err != nil {
		log.Fatal(err)
	}
	store := queue.NewDBStore(db)
	if err := store.Migrate(); err != nil {
		log.Fatal(err)
	}

	sms := memory.New()
	sms.FailNext(notijet.Fault("memory", "gateway timeout", nil)) // the first attempt fails
	n := notijet.NewNotifier(notijet.WithLogger(quiet))
	n.MustRegister("sms", sms)

	// A simulated clock lets the example skip the retry backoff.
	now := time.Now()
	q := queue.New(store, n, queue.WithLogger(quiet), queue.WithClock(func() time.Time { return now }),
		queue.Backoff(30*time.Second, 10*time.Minute))

	repo := gormx.NewBaseRepository(db)
	orders := gormx.NewTableFor[Order](&repo)
	placeOrder := func(id string, fail bool) error {
		return repo.RunTx(ctx, func(ctx context.Context) error {
			if err := orders.Create(ctx, &Order{ID: id, Phone: "09121234567"}); err != nil {
				return err
			}
			msg := &notijet.Message{ID: "order-" + id + "-placed", To: []string{"09121234567"}, Text: "Order " + id + " received."}
			if _, err := q.Enqueue(ctx, notijet.ChannelSMS, msg); err != nil {
				return err
			}
			if fail {
				return errors.New("payment declined")
			}
			return nil
		})
	}

	fmt.Println("order 1:", placeOrder("1", false))
	fmt.Println("order 2:", placeOrder("2", true), "(rolled back, and its SMS with it)")
	if rec, _ := q.Get(ctx, "order-2-placed"); rec == nil {
		fmt.Println("order-2-placed was never queued")
	}

	// First pass: the delivery fails and is scheduled for a retry.
	if _, err := q.Process(ctx); err != nil {
		log.Fatal(err)
	}
	rec, _ := q.Get(ctx, "order-1-placed")
	fmt.Printf("after pass 1: %s, attempt %d, next try in %s (%s)\n",
		rec.Status, rec.Attempts, rec.NextAttemptAt.Sub(now).Round(time.Second), rec.LastError)

	// Later: the retry succeeds.
	now = now.Add(time.Minute)
	sent, err := q.Process(ctx)
	if err != nil {
		log.Fatal(err)
	}
	rec, _ = q.Get(ctx, "order-1-placed")
	receipts, _ := rec.DecodeReceipts()
	fmt.Printf("after pass 2: %s (%d sent), via %s, message id %s\n", rec.Status, sent, rec.SentVia, receipts[0].MessageID)
	fmt.Println("SMS delivered:", sms.Len())

	// In a long-running service, run the worker instead of calling Process:
	//	go q.Run(ctx)
}
