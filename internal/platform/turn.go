package platform

import "time"

// TurnMessage is what the gateway publishes to AGENT_TURNS for every user message.
type TurnMessage struct {
	SessionID string    `json:"session_id"`
	MessageID string    `json:"message_id"`
	Text      string    `json:"text"`
	SentAt    time.Time `json:"sent_at"`
}

// Event is one item of the stream the agent publishes per session and the
// gateway relays over SSE.
type Event struct {
	// Type is one of: status, token, plan, done, error.
	Type    string `json:"type"`
	Stage   string `json:"stage,omitempty"`
	Text    string `json:"text,omitempty"`
	Plan    any    `json:"plan,omitempty"`
	Message string `json:"message,omitempty"`
}
