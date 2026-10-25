// Command notifier consumes booking events and sends notifications.
package main

import (
	"encoding/json"
	"log/slog"
	"os"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"horkilion/internal/analytics"
	"horkilion/internal/platform"
)

func main() {
	platform.HandleHealthcheck()
	platform.NewLogger("notifier")
	if err := run(); err != nil {
		slog.Error("notifier failed", "err", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := platform.SignalContext()
	defer stop()

	nc, err := platform.ConnectNATS("notifier")
	if err != nil {
		return err
	}
	defer nc.Drain()
	js, err := jetstream.New(nc)
	if err != nil {
		return err
	}
	if err := platform.EnsureStreams(ctx, js); err != nil {
		return err
	}
	cons, err := js.CreateOrUpdateConsumer(ctx, platform.StreamBookings, jetstream.ConsumerConfig{
		Durable:       "notifier",
		FilterSubject: platform.SubjectBookingCreated,
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       30 * time.Second,
		MaxDeliver:    5,
	})
	if err != nil {
		return err
	}

	an := analytics.New(os.Getenv("CLICKHOUSE_URL"), platform.Env("CLICKHOUSE_USER", "default"), os.Getenv("CLICKHOUSE_PASSWORD"))

	platform.StartOps(ctx, platform.NATSReady(nc))
	cc, err := cons.Consume(func(msg jetstream.Msg) {
		var ev platform.BookingCreated
		if err := json.Unmarshal(msg.Data(), &ev); err != nil {
			slog.Error("malformed booking event, dropping", "err", err)
			_ = msg.Term()
			return
		}
		// impulse: notifications are logged only, show them on the site when post-purchase lands.
		slog.Info("notification: booking confirmed",
			"order_id", ev.ID, "session_id", ev.SessionID, "items", len(ev.Items), "total_rub", ev.TotalRub)
		an.Track("checkout_completed", ev.SessionID, "checkout", map[string]any{
			"order_id": ev.ID, "total_rub": ev.TotalRub, "items": len(ev.Items),
		})
		_ = msg.Ack()
	})
	if err != nil {
		return err
	}
	defer cc.Stop()

	<-ctx.Done()
	return nil
}
