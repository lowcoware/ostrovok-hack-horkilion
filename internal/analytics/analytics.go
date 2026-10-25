// Package analytics writes product funnel events to the analytics database in
// ClickHouse over its HTTP interface.
package analytics

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"time"
)

// Client sends events in the background. A nil *Client drops events, so
// analytics never breaks a user flow.
type Client struct {
	endpoint string
	user     string
	password string
	http     *http.Client
}

// New returns nil when baseURL is empty.
func New(baseURL, user, password string) *Client {
	if baseURL == "" {
		return nil
	}
	q := url.Values{
		"query":                  {"INSERT INTO analytics.events FORMAT JSONEachRow"},
		"date_time_input_format": {"best_effort"},
	}
	return &Client{
		endpoint: baseURL + "/?" + q.Encode(),
		user:     user,
		password: password,
		http:     &http.Client{Timeout: 3 * time.Second},
	}
}

type event struct {
	EventTime time.Time `json:"event_time"`
	Event     string    `json:"event"`
	SessionID string    `json:"session_id"`
	Stage     string    `json:"stage"`
	Props     string    `json:"props"`
}

// Track records one funnel event, for example stage_entered or checkout_completed.
// impulse: one HTTP insert per event, batch inserts when traffic passes a few events per second.
func (c *Client) Track(name, sessionID, stage string, props map[string]any) {
	if c == nil {
		return
	}
	propsJSON, _ := json.Marshal(props)
	body, err := json.Marshal(event{EventTime: time.Now().UTC(), Event: name, SessionID: sessionID, Stage: stage, Props: string(propsJSON)})
	if err != nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := c.send(ctx, body); err != nil {
			slog.Warn("analytics event dropped", "event", name, "err", err)
		}
	}()
}

func (c *Client) send(ctx context.Context, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.SetBasicAuth(c.user, c.password)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("clickhouse returned %s", resp.Status)
	}
	return nil
}
