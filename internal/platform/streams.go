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
