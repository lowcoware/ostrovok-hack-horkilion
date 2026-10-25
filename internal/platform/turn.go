package platform

import (
	"encoding/json"
	"time"
)

// TurnMessage is what the gateway publishes to AGENT_TURNS for every user
// message and every plan patch from the interface.
type TurnMessage struct {
	SessionID string `json:"session_id"`
	// MessageID becomes the turn_id of the events of this turn.
	MessageID string `json:"message_id"`
	OwnerID   string `json:"owner_id"`
	// Exactly one of Text and Patch is set. Patch is a PlanPatch from the API.
	Text   string          `json:"text,omitempty"`
	Patch  json.RawMessage `json:"patch,omitempty"`
	SentAt time.Time       `json:"sent_at"`
}

// Event is one item of the stream the agent publishes per session and the
// gateway relays over SSE as is. The shape is AgentEvent in api/openapi.yaml.
type Event struct {
	// Type is one of: status, token, plan, clarify, done, error.
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
	TurnID    string `json:"turn_id"`
	Seq       int64  `json:"seq"`
	Data      any    `json:"data"`
}

type StatusData struct {
	Stage string `json:"stage"`
	Text  string `json:"text"`
}

type TokenData struct {
	Text string `json:"text"`
}

type ClarifyData struct {
	Question     string   `json:"question"`
	QuickReplies []string `json:"quick_replies"`
}

type DoneData struct {
	Text  string `json:"text"`
	Stage string `json:"stage"`
}

type ErrorData struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
