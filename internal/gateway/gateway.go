// Package gateway is the HTTP and SSE entry point for the web frontend and
// any other client of the agent API.
package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"golang.org/x/time/rate"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	bookingv1 "horkilion/gen/booking/v1"
	"horkilion/internal/platform"
)

const (
	maxBodyBytes   = 16 << 10
	maxMessageLen  = 2000
	sseHeartbeat   = 15 * time.Second
	bookingTimeout = 10 * time.Second
)

// Gateway holds the dependencies of the HTTP API.
type Gateway struct {
	nc      *nats.Conn
	js      jetstream.JetStream
	booking bookingv1.BookingServiceClient
	limiter *ipLimiter
}

func New(nc *nats.Conn, js jetstream.JetStream, booking bookingv1.BookingServiceClient) *Gateway {
	return &Gateway{nc: nc, js: js, booking: booking, limiter: newIPLimiter(rate.Limit(2), 10)}
}

// Router builds the HTTP routes.
func (g *Gateway) Router() *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery(), limitBody, g.rateLimit)

	api := r.Group("/api")
	api.POST("/sessions", g.createSession)
	api.POST("/sessions/:id/messages", g.postMessage)
	api.GET("/sessions/:id/events", g.events)
	api.POST("/sessions/:id/checkout", g.checkout)
	return r
}

func (g *Gateway) createSession(c *gin.Context) {
	c.JSON(http.StatusCreated, gin.H{"session_id": uuid.NewString()})
}

func (g *Gateway) postMessage(c *gin.Context) {
	sessionID, ok := sessionParam(c)
	if !ok {
		return
	}
	var body struct {
		Text string `json:"text"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		badRequest(c, "body must be JSON with a text field")
		return
	}
	text := strings.TrimSpace(body.Text)
	if text == "" || utf8.RuneCountInString(text) > maxMessageLen {
		badRequest(c, fmt.Sprintf("text must be 1 to %d characters", maxMessageLen))
		return
	}

	turn := platform.TurnMessage{SessionID: sessionID, MessageID: uuid.NewString(), Text: text, SentAt: time.Now().UTC()}
	data, _ := json.Marshal(turn)
	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()
	if _, err := g.js.Publish(ctx, platform.SubjectAgentTurn+sessionID, data, jetstream.WithMsgID(turn.MessageID)); err != nil {
		slog.Error("publish turn failed", "session_id", sessionID, "err", err)
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "agent queue is unavailable"})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"message_id": turn.MessageID})
}

// events relays the session's agent events as Server-Sent Events. The client
// opens it before sending a message: events published earlier are not replayed.
func (g *Gateway) events(c *gin.Context) {
	sessionID, ok := sessionParam(c)
	if !ok {
		return
	}
	ch := make(chan *nats.Msg, 256)
	sub, err := g.nc.ChanSubscribe(platform.SessionEventsSubject(sessionID), ch)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "event stream is unavailable"})
		return
	}
	defer sub.Unsubscribe()

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	c.Writer.Flush()

	heartbeat := time.NewTicker(sseHeartbeat)
	defer heartbeat.Stop()
	c.Stream(func(w io.Writer) bool {
		select {
		case <-c.Request.Context().Done():
			return false
		case msg := <-ch:
			fmt.Fprintf(w, "data: %s\n\n", msg.Data)
		case <-heartbeat.C:
			fmt.Fprint(w, ": ping\n\n")
		}
		return true
	})
}

func (g *Gateway) checkout(c *gin.Context) {
	sessionID, ok := sessionParam(c)
	if !ok {
		return
	}
	key := c.GetHeader("Idempotency-Key")
	if _, err := uuid.Parse(key); err != nil {
		badRequest(c, "Idempotency-Key header must be a UUID")
		return
	}
	var body struct {
		HotelID   string `json:"hotel_id"`
		CheckIn   string `json:"check_in"`
		Nights    int32  `json:"nights"`
		Adults    int32  `json:"adults"`
		Children  int32  `json:"children"`
		GuestName string `json:"guest_name"`
		WithPets  bool   `json:"with_pets"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		badRequest(c, "body must be JSON")
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), bookingTimeout)
	defer cancel()
	order, err := g.booking.CreateOrder(ctx, &bookingv1.CreateOrderRequest{
		IdempotencyKey: key,
		SessionId:      sessionID,
		HotelId:        body.HotelID,
		CheckIn:        body.CheckIn,
		Nights:         body.Nights,
		Adults:         body.Adults,
		Children:       body.Children,
		GuestName:      body.GuestName,
		WithPets:       body.WithPets,
	})
	if err != nil {
		writeGRPCError(c, err)
		return
	}
	c.JSON(http.StatusOK, order)
}

func sessionParam(c *gin.Context) (string, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		badRequest(c, "session id must be a UUID")
		return "", false
	}
	return id.String(), true
}

func badRequest(c *gin.Context, msg string) {
	c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": msg})
}

func writeGRPCError(c *gin.Context, err error) {
	st, _ := status.FromError(err)
	code := http.StatusBadGateway
	switch st.Code() {
	case codes.InvalidArgument:
		code = http.StatusBadRequest
	case codes.NotFound:
		code = http.StatusNotFound
	case codes.DeadlineExceeded, codes.Unavailable:
		code = http.StatusServiceUnavailable
	}
	if code == http.StatusBadGateway {
		slog.Error("booking call failed", "err", err)
		c.JSON(code, gin.H{"error": "booking failed"})
		return
	}
	c.JSON(code, gin.H{"error": st.Message()})
}

func limitBody(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBodyBytes)
	c.Next()
}

func (g *Gateway) rateLimit(c *gin.Context) {
	if c.Request.Method == http.MethodGet {
		c.Next()
		return
	}
	if !g.limiter.allow(c.ClientIP()) {
		c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "too many requests"})
		return
	}
	c.Next()
}

// ipLimiter is a token bucket per client IP.
// impulse: in-memory limiter per gateway instance, move to the edge (Caddy) or NATS KV when the gateway runs more than one replica.
type ipLimiter struct {
	mu      sync.Mutex
	r       rate.Limit
	burst   int
	buckets map[string]*rate.Limiter
}

func newIPLimiter(r rate.Limit, burst int) *ipLimiter {
	return &ipLimiter{r: r, burst: burst, buckets: map[string]*rate.Limiter{}}
}

func (l *ipLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[ip]
	if !ok {
		if len(l.buckets) > 10000 {
			l.buckets = map[string]*rate.Limiter{}
		}
		b = rate.NewLimiter(l.r, l.burst)
		l.buckets[ip] = b
	}
	return b.Allow()
}
