package platform

import (
	"context"
	"fmt"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// Stream and subject names shared between publishers and consumers.
const (
	StreamAgentTurns = "AGENT_TURNS"
	// SubjectAgentTurn is followed by the session ID.
	SubjectAgentTurn = "agent.turns."

	StreamBookings        = "BOOKINGS"
	SubjectBookingCreated = "booking.created"
)

// SessionEventsSubject is the core NATS subject the agent publishes turn events
// to and the gateway relays over SSE.
func SessionEventsSubject(sessionID string) string {
	return "session." + sessionID + ".events"
}

// EnsureStreams creates or updates the JetStream streams. Every service that
// touches a stream calls it, so start order does not matter.
func EnsureStreams(ctx context.Context, js jetstream.JetStream) error {
	streams := []jetstream.StreamConfig{
		{
			Name:       StreamAgentTurns,
			Subjects:   []string{SubjectAgentTurn + ">"},
			Retention:  jetstream.WorkQueuePolicy,
			MaxAge:     24 * time.Hour,
			Duplicates: 2 * time.Minute,
		},
		{
			Name:       StreamBookings,
			Subjects:   []string{"booking.>"},
			MaxAge:     30 * 24 * time.Hour,
			Duplicates: 10 * time.Minute,
		},
	}
	for _, cfg := range streams {
		if _, err := js.CreateOrUpdateStream(ctx, cfg); err != nil {
			return fmt.Errorf("ensure stream %s: %w", cfg.Name, err)
		}
	}
	return nil
}

// BookingCreated is the payload of SubjectBookingCreated.
type BookingCreated struct {
	ID        string            `json:"id"`
	SessionID string            `json:"session_id"`
	OwnerID   string            `json:"owner_id"`
	TotalRub  int64             `json:"total_rub"`
	Items     []BookingItemInfo `json:"items"`
}

// FullTrip reports whether the order is a whole trip: a hotel, both
// journeys and both transfers (the phase 1 target, spec 0003).
func (b BookingCreated) FullTrip() bool {
	kinds := map[string]int{}
	for _, it := range b.Items {
		kinds[it.Kind]++
	}
	return kinds["stay"] >= 1 && kinds["journey"] >= 2 && kinds["transfer"] >= 2
}

type BookingItemInfo struct {
	Kind     string `json:"kind"`
	Title    string `json:"title"`
	PriceRub int64  `json:"price_rub"`
}
