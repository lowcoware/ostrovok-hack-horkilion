package booking

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go/jetstream"
)

// RunOutboxRelay publishes unpublished outbox rows to JetStream until ctx is
// cancelled. The row ID is the JetStream message ID, so a crash between publish
// and update produces a deduplicated retry instead of a double event.
func RunOutboxRelay(ctx context.Context, db *pgxpool.Pool, js jetstream.JetStream) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := relayBatch(ctx, db, js); err != nil {
				slog.Error("outbox relay batch failed", "err", err)
			}
		}
	}
}

func relayBatch(ctx context.Context, db *pgxpool.Pool, js jetstream.JetStream) error {
	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(ctx, `
		SELECT id, subject, payload FROM booking.outbox
		WHERE published_at IS NULL
		ORDER BY id
		LIMIT 100
		FOR UPDATE SKIP LOCKED`)
	if err != nil {
		return err
	}
	type entry struct {
		id      int64
		subject string
		payload []byte
	}
	var batch []entry
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.id, &e.subject, &e.payload); err != nil {
			rows.Close()
			return err
		}
		batch = append(batch, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	var published []int64
	for _, e := range batch {
		pubCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		_, err := js.Publish(pubCtx, e.subject, e.payload, jetstream.WithMsgID("outbox-"+strconv.FormatInt(e.id, 10)))
		cancel()
		if err != nil {
			slog.Warn("outbox publish failed, will retry", "id", e.id, "err", err)
			break
		}
		published = append(published, e.id)
	}
	if len(published) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `UPDATE booking.outbox SET published_at = now() WHERE id = ANY($1)`, published); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
