package agent

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"horkilion/internal/agent/plan"
)

// ErrAlreadyProcessed means the turn message was handled by an earlier delivery.
var ErrAlreadyProcessed = errors.New("turn already processed")

// Store keeps versioned plans and chat history in PostgreSQL.
type Store struct {
	db *pgxpool.Pool
}

func NewStore(db *pgxpool.Pool) *Store { return &Store{db: db} }

type ChatMessage struct {
	Role    string
	Content string
}

// LoadPlan returns the latest plan version, or a new plan for a new session.
// A plan stored by an older schema keeps the fields that still exist.
func (s *Store) LoadPlan(ctx context.Context, sessionID string) (plan.TripPlan, error) {
	var raw []byte
	err := s.db.QueryRow(ctx,
		`SELECT plan FROM agent.trip_plans WHERE session_id = $1 ORDER BY version DESC LIMIT 1`, sessionID,
	).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return plan.New(sessionID), nil
	}
	if err != nil {
		return plan.TripPlan{}, err
	}
	p := plan.New(sessionID)
	return p, json.Unmarshal(raw, &p)
}

func (s *Store) Processed(ctx context.Context, messageID string) (bool, error) {
	var exists bool
	err := s.db.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM agent.processed_messages WHERE message_id = $1)`, messageID,
	).Scan(&exists)
	return exists, err
}

// History returns the last limit messages of a session, oldest first.
func (s *Store) History(ctx context.Context, sessionID string, limit int) ([]ChatMessage, error) {
	rows, err := s.db.Query(ctx, `
		SELECT role, content FROM (
			SELECT id, role, content FROM agent.messages WHERE session_id = $1 ORDER BY id DESC LIMIT $2
		) recent ORDER BY id`, sessionID, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (ChatMessage, error) {
		var m ChatMessage
		return m, row.Scan(&m.Role, &m.Content)
	})
}

// SaveTurn stores the next plan version and both messages atomically, marking
// the turn message as processed.
func (s *Store) SaveTurn(ctx context.Context, p *plan.TripPlan, messageID, userText, reply string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx,
		`INSERT INTO agent.processed_messages (message_id) VALUES ($1) ON CONFLICT DO NOTHING`, messageID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrAlreadyProcessed
	}

	p.Version++
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO agent.trip_plans (session_id, version, plan) VALUES ($1, $2, $3)`,
		p.SessionID, p.Version, raw); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO agent.messages (session_id, role, content) VALUES ($1, 'user', $2), ($1, 'assistant', $3)`,
		p.SessionID, userText, reply); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
