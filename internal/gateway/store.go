package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"horkilion/internal/agent/plan"
)

// errNotFound means the session does not exist or belongs to another owner.
var errNotFound = errors.New("not found")

// Store is what the gateway keeps and reads in PostgreSQL.
type Store interface {
	CreateSession(ctx context.Context, owner string) (string, error)
	// CheckOwner returns errNotFound unless the session belongs to owner.
	CheckOwner(ctx context.Context, sessionID, owner string) error
	ListSessions(ctx context.Context, owner string) ([]SessionSummary, error)
	// LoadPlan returns the latest plan, or a new empty plan before the first turn.
	LoadPlan(ctx context.Context, sessionID string) (plan.TripPlan, error)
	Messages(ctx context.Context, sessionID string) ([]ChatMessage, error)
}

type SessionSummary struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Stage     string    `json:"stage"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type ChatMessage struct {
	Role      string    `json:"role"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"created_at"`
}

// PGStore owns gateway.sessions and reads plans and messages of the agent schema.
// impulse: the gateway reads agent tables directly, put them behind an agent read API when the agent's storage changes or moves to its own database.
type PGStore struct {
	db *pgxpool.Pool
}

func NewPGStore(db *pgxpool.Pool) *PGStore { return &PGStore{db: db} }

func (s *PGStore) CreateSession(ctx context.Context, owner string) (string, error) {
	var id string
	err := s.db.QueryRow(ctx,
		`INSERT INTO gateway.sessions (id, owner_id) VALUES (gen_random_uuid(), $1) RETURNING id::text`, owner).Scan(&id)
	return id, err
}

func (s *PGStore) CheckOwner(ctx context.Context, sessionID, owner string) error {
	var ok bool
	err := s.db.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM gateway.sessions WHERE id = $1 AND owner_id = $2)`, sessionID, owner).Scan(&ok)
	if err != nil {
		return err
	}
	if !ok {
		return errNotFound
	}
	return nil
}

func (s *PGStore) ListSessions(ctx context.Context, owner string) ([]SessionSummary, error) {
	// impulse: no pagination, add a cursor when an owner has more than a hundred trips.
	rows, err := s.db.Query(ctx, `
		SELECT s.id::text, coalesce(p.plan->'constraints'->>'destination', ''), coalesce(p.plan->>'stage', 'clarify'),
		       s.created_at, coalesce(p.created_at, s.created_at)
		FROM gateway.sessions s
		LEFT JOIN LATERAL (
			SELECT plan, created_at FROM agent.trip_plans WHERE session_id = s.id ORDER BY version DESC LIMIT 1
		) p ON true
		WHERE s.owner_id = $1
		ORDER BY s.created_at DESC
		LIMIT 100`, owner)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (SessionSummary, error) {
		var s SessionSummary
		return s, row.Scan(&s.ID, &s.Title, &s.Stage, &s.CreatedAt, &s.UpdatedAt)
	})
}

func (s *PGStore) LoadPlan(ctx context.Context, sessionID string) (plan.TripPlan, error) {
	var raw []byte
	err := s.db.QueryRow(ctx,
		`SELECT plan FROM agent.trip_plans WHERE session_id = $1 ORDER BY version DESC LIMIT 1`, sessionID).Scan(&raw)
	p := plan.New(sessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	return p, json.Unmarshal(raw, &p)
}

func (s *PGStore) Messages(ctx context.Context, sessionID string) ([]ChatMessage, error) {
	rows, err := s.db.Query(ctx,
		`SELECT role, content, created_at FROM agent.messages WHERE session_id = $1 ORDER BY id LIMIT 500`, sessionID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (ChatMessage, error) {
		var m ChatMessage
		return m, row.Scan(&m.Role, &m.Text, &m.CreatedAt)
	})
}
