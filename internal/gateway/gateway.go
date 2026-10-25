// Package gateway serves the external API (api/openapi.yaml) to the web
// frontend and any other client of the agent: owner cookie, session ownership,
// turns into the agent queue, SSE relay and checkout through booking.
package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	"horkilion/internal/agent/plan"
	"horkilion/internal/platform"
)

const (
	maxBodyBytes   = 16 << 10
	maxMessageLen  = 2000
	sseHeartbeat   = 15 * time.Second
	bookingTimeout = 10 * time.Second

	ownerCookie = "horkilion_owner"
	ownerKey    = "owner"
)

// Turns puts turn messages into the agent queue.
type Turns interface {
	Publish(ctx context.Context, msg platform.TurnMessage) error
}

// Events subscribes to the agent events of a session.
type Events interface {
	Subscribe(sessionID string, ch chan<- []byte) (unsubscribe func(), err error)
}

// Gateway holds the dependencies of the HTTP API.
type Gateway struct {
	store   Store
	turns   Turns
	events  Events
	booking bookingv1.BookingServiceClient
	limiter *ipLimiter
}

func New(store Store, turns Turns, events Events, booking bookingv1.BookingServiceClient) *Gateway {
	return &Gateway{store: store, turns: turns, events: events, booking: booking, limiter: newIPLimiter(rate.Limit(2), 10)}
}

// Router builds the HTTP routes.
func (g *Gateway) Router() *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery(), limitBody, g.rateLimit)

	api := r.Group("/api/v1", owner)
	api.POST("/auth/register", notImplemented)
	api.POST("/auth/login", notImplemented)
	api.POST("/auth/logout", notImplemented)
	api.GET("/me", me)

	api.POST("/sessions", g.createSession)
	api.GET("/sessions", g.listSessions)
	own := api.Group("/sessions/:id", g.ownSession)
	own.GET("", g.getSession)
	own.POST("/messages", g.postMessage)
	own.POST("/plan", g.patchPlan)
	own.GET("/events", g.sessionEvents)
	own.POST("/checkout", g.checkout)

	api.GET("/orders", g.listOrders)
	api.GET("/orders/:id", g.getOrder)
	api.GET("/sources", listSources)
	return r
}

// owner identifies the request owner by the anonymous cookie and sets the
// cookie on the first request.
// impulse: anonymous owners only, accounts from ADR 0002 replace the cookie value with a server session in phase 2.
func owner(c *gin.Context) {
	id, err := c.Cookie(ownerCookie)
	if _, perr := uuid.Parse(id); err != nil || perr != nil {
		id = uuid.NewString()
		secure := c.Request.TLS != nil || c.GetHeader("X-Forwarded-Proto") == "https"
		c.SetSameSite(http.SameSiteLaxMode)
		c.SetCookie(ownerCookie, id, 365*24*3600, "/", "", secure, true)
	}
	c.Set(ownerKey, "anon:"+id)
	c.Next()
}

func ownerOf(c *gin.Context) string { return c.GetString(ownerKey) }

// ownSession aborts with 404 unless the session belongs to the owner, so that
// another owner cannot even learn that the id exists.
func (g *Gateway) ownSession(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		apiError(c, http.StatusBadRequest, "bad_request", "session id must be a UUID")
		return
	}
	switch err := g.store.CheckOwner(c.Request.Context(), id.String(), ownerOf(c)); {
	case errors.Is(err, errNotFound):
		apiError(c, http.StatusNotFound, "not_found", "session not found")
		return
	case err != nil:
		internalError(c, "check session owner", err)
		return
	}
	c.Set("session_id", id.String())
	c.Next()
}

func me(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"owner_id": ownerOf(c), "anonymous": true})
}

func (g *Gateway) createSession(c *gin.Context) {
	id, err := g.store.CreateSession(c.Request.Context(), ownerOf(c))
	if err != nil {
		internalError(c, "create session", err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"session_id": id})
}

func (g *Gateway) listSessions(c *gin.Context) {
	sessions, err := g.store.ListSessions(c.Request.Context(), ownerOf(c))
	if err != nil {
		internalError(c, "list sessions", err)
		return
	}
	if sessions == nil {
		sessions = []SessionSummary{}
	}
	c.JSON(http.StatusOK, gin.H{"sessions": sessions})
}

func (g *Gateway) getSession(c *gin.Context) {
	ctx, id := c.Request.Context(), c.GetString("session_id")
	p, err := g.store.LoadPlan(ctx, id)
	if err != nil {
		internalError(c, "load plan", err)
		return
	}
	msgs, err := g.store.Messages(ctx, id)
	if err != nil {
		internalError(c, "load messages", err)
		return
	}
	if msgs == nil {
		msgs = []ChatMessage{}
	}
	c.JSON(http.StatusOK, gin.H{"plan": p, "messages": msgs})
}

func (g *Gateway) postMessage(c *gin.Context) {
	var body struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(c.Request.Body).Decode(&body); err != nil {
		apiError(c, http.StatusBadRequest, "bad_request", "body must be JSON with a text field")
		return
	}
	text := strings.TrimSpace(body.Text)
	if text == "" || utf8.RuneCountInString(text) > maxMessageLen {
		apiError(c, http.StatusBadRequest, "bad_request", fmt.Sprintf("text must be 1 to %d characters", maxMessageLen))
		return
	}
	g.publishTurn(c, platform.TurnMessage{Text: text})
}

func (g *Gateway) patchPlan(c *gin.Context) {
	var patch plan.Patch
	dec := json.NewDecoder(c.Request.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&patch); err != nil {
		apiError(c, http.StatusBadRequest, "bad_request", "body must be a PlanPatch: "+err.Error())
		return
	}
	if err := patch.Validate(); err != nil {
		apiError(c, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	raw, _ := json.Marshal(patch)
	g.publishTurn(c, platform.TurnMessage{Patch: raw})
}

func (g *Gateway) publishTurn(c *gin.Context, turn platform.TurnMessage) {
	turn.SessionID, turn.OwnerID = c.GetString("session_id"), ownerOf(c)
	turn.MessageID, turn.SentAt = uuid.NewString(), time.Now().UTC()
	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()
	if err := g.turns.Publish(ctx, turn); err != nil {
		slog.Error("publish turn failed", "session_id", turn.SessionID, "err", err)
		apiError(c, http.StatusServiceUnavailable, "unavailable", "agent queue is unavailable")
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"message_id": turn.MessageID})
}

// sessionEvents relays the session's agent events as Server-Sent Events. The
// client opens it before sending a message: events published earlier are not
// replayed, the client reloads the plan with GET /sessions/{id} instead.
func (g *Gateway) sessionEvents(c *gin.Context) {
	ch := make(chan []byte, 256)
	unsubscribe, err := g.events.Subscribe(c.GetString("session_id"), ch)
	if err != nil {
		apiError(c, http.StatusServiceUnavailable, "unavailable", "event stream is unavailable")
		return
	}
	defer unsubscribe()

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
		case data := <-ch:
			fmt.Fprintf(w, "data: %s\n\n", data)
		case <-heartbeat.C:
			fmt.Fprint(w, ": ping\n\n")
		}
		return true
	})
}

func (g *Gateway) checkout(c *gin.Context) {
	key := c.GetHeader("Idempotency-Key")
	if _, err := uuid.Parse(key); err != nil {
		apiError(c, http.StatusBadRequest, "bad_request", "Idempotency-Key header must be a UUID")
		return
	}
	var body struct {
		GuestName string `json:"guest_name"`
	}
	if err := json.NewDecoder(c.Request.Body).Decode(&body); err != nil {
		apiError(c, http.StatusBadRequest, "bad_request", "body must be JSON with a guest_name field")
		return
	}
	guest := strings.TrimSpace(body.GuestName)
	if guest == "" || utf8.RuneCountInString(guest) > 200 {
		apiError(c, http.StatusBadRequest, "bad_request", "guest_name must be 1 to 200 characters")
		return
	}

	sessionID := c.GetString("session_id")
	p, err := g.store.LoadPlan(c.Request.Context(), sessionID)
	if err != nil {
		internalError(c, "load plan", err)
		return
	}
	items, err := cartItems(p)
	if err != nil {
		apiError(c, http.StatusConflict, err.Error(), cartMessages[err.Error()])
		return
	}

	ctx, cancel := context.WithTimeout(platform.WithOwner(c.Request.Context(), ownerOf(c)), bookingTimeout)
	defer cancel()
	order, err := g.booking.CreateOrder(ctx, &bookingv1.CreateOrderRequest{
		IdempotencyKey: key, SessionId: sessionID, GuestName: guest, Items: items,
	})
	if err != nil {
		g.bookingError(c, err)
		return
	}
	c.JSON(http.StatusOK, orderJSON(order))
}

var cartMessages = map[string]string{
	"nothing_selected":  "в плане нет выбранных вариантов",
	"offer_unavailable": "выбранный вариант больше не предлагается, выберите другой",
}

// cartItems builds order items from the selected options of every component.
func cartItems(p plan.TripPlan) ([]*bookingv1.OrderItem, error) {
	c := p.Constraints
	var items []*bookingv1.OrderItem
	for _, name := range plan.ComponentNames {
		sel := p.Components.Get(name).Selected()
		if sel == nil {
			continue
		}
		for _, f := range sel.Flags {
			if f == plan.FlagSelectedUnavailable {
				return nil, errors.New("offer_unavailable")
			}
		}
		item := &bookingv1.OrderItem{OfferId: sel.ID, PriceRub: sel.PriceRub, Title: plan.OptionTitle(*sel)}
		switch sel.Kind {
		case plan.KindStay:
			item.Kind, item.Stay = bookingv1.ItemKind_ITEM_KIND_STAY, c.StayProto()
		case plan.KindJourney:
			item.Kind, item.Travelers = bookingv1.ItemKind_ITEM_KIND_JOURNEY, c.TravelersProto()
		case plan.KindTransfer:
			item.Kind, item.Travelers = bookingv1.ItemKind_ITEM_KIND_TRANSFER, c.TravelersProto()
		}
		items = append(items, item)
	}
	if len(items) == 0 {
		return nil, errors.New("nothing_selected")
	}
	return items, nil
}

func (g *Gateway) listOrders(c *gin.Context) {
	ctx, cancel := context.WithTimeout(platform.WithOwner(c.Request.Context(), ownerOf(c)), bookingTimeout)
	defer cancel()
	resp, err := g.booking.ListOrders(ctx, &bookingv1.ListOrdersRequest{})
	if err != nil {
		g.bookingError(c, err)
		return
	}
	orders := make([]order, 0, len(resp.GetOrders()))
	for _, o := range resp.GetOrders() {
		orders = append(orders, orderJSON(o))
	}
	c.JSON(http.StatusOK, gin.H{"orders": orders})
}

func (g *Gateway) getOrder(c *gin.Context) {
	if _, err := uuid.Parse(c.Param("id")); err != nil {
		apiError(c, http.StatusBadRequest, "bad_request", "order id must be a UUID")
		return
	}
	ctx, cancel := context.WithTimeout(platform.WithOwner(c.Request.Context(), ownerOf(c)), bookingTimeout)
	defer cancel()
	o, err := g.booking.GetOrder(ctx, &bookingv1.GetOrderRequest{Id: c.Param("id")})
	if err != nil {
		g.bookingError(c, err)
		return
	}
	c.JSON(http.StatusOK, orderJSON(o))
}

func listSources(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"providers": providers})
}

func notImplemented(c *gin.Context) {
	apiError(c, http.StatusNotImplemented, "not_implemented", "accounts arrive in phase 2")
}

type order struct {
	ID        string      `json:"id"`
	SessionID string      `json:"session_id"`
	Status    string      `json:"status"`
	Items     []orderItem `json:"items"`
	TotalRub  int64       `json:"total_rub"`
	GuestName string      `json:"guest_name"`
	CreatedAt string      `json:"created_at"`
}

type orderItem struct {
	Kind     string `json:"kind"`
	OfferID  string `json:"offer_id"`
	Title    string `json:"title"`
	PriceRub int64  `json:"price_rub"`
}

var itemKinds = map[bookingv1.ItemKind]string{
	bookingv1.ItemKind_ITEM_KIND_STAY:     plan.KindStay,
	bookingv1.ItemKind_ITEM_KIND_JOURNEY:  plan.KindJourney,
	bookingv1.ItemKind_ITEM_KIND_TRANSFER: plan.KindTransfer,
}

func orderJSON(o *bookingv1.Order) order {
	out := order{
		ID: o.GetId(), SessionID: o.GetSessionId(), Status: o.GetStatus(), TotalRub: o.GetTotalRub(),
		GuestName: o.GetGuestName(), CreatedAt: o.GetCreatedAt(), Items: []orderItem{},
	}
	for _, it := range o.GetItems() {
		out.Items = append(out.Items, orderItem{Kind: itemKinds[it.GetKind()], OfferID: it.GetOfferId(), Title: it.GetTitle(), PriceRub: it.GetPriceRub()})
	}
	return out
}

// bookingError maps a booking gRPC error onto the API error format.
func (g *Gateway) bookingError(c *gin.Context, err error) {
	st := status.Convert(err)
	switch st.Code() {
	case codes.InvalidArgument:
		apiError(c, http.StatusBadRequest, "bad_request", st.Message())
	case codes.NotFound:
		apiError(c, http.StatusNotFound, "not_found", "order not found")
	case codes.FailedPrecondition:
		for _, d := range st.Details() {
			if pc, ok := d.(*bookingv1.PriceChanged); ok {
				c.AbortWithStatusJSON(http.StatusConflict, gin.H{"error": gin.H{
					"code": "price_changed", "message": "цены изменились, проверьте план", "details": priceChangedJSON(pc),
				}})
				return
			}
		}
		apiError(c, http.StatusConflict, "offer_unavailable", cartMessages["offer_unavailable"])
	case codes.DeadlineExceeded, codes.Unavailable, codes.Unimplemented:
		apiError(c, http.StatusServiceUnavailable, "unavailable", "booking is unavailable, try again later")
	default:
		internalError(c, "booking call", err)
	}
}

func priceChangedJSON(pc *bookingv1.PriceChanged) gin.H {
	items := make([]gin.H, 0, len(pc.GetItems()))
	for _, it := range pc.GetItems() {
		items = append(items, gin.H{
			"index": it.GetIndex(), "kind": itemKinds[it.GetKind()], "offer_id": it.GetOfferId(),
			"seen_price_rub": it.GetSeenPriceRub(), "actual_price_rub": it.GetActualPriceRub(),
		})
	}
	return gin.H{"items": items}
}

func apiError(c *gin.Context, code int, errCode, msg string) {
	c.AbortWithStatusJSON(code, gin.H{"error": gin.H{"code": errCode, "message": msg}})
}

func internalError(c *gin.Context, what string, err error) {
	slog.Error(what+" failed", "err", err)
	apiError(c, http.StatusInternalServerError, "internal", "internal error")
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
		apiError(c, http.StatusTooManyRequests, "rate_limited", "too many requests")
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

// NATSTurns publishes turns to the AGENT_TURNS stream, deduplicated by message id.
type NATSTurns struct{ JS jetstream.JetStream }

func (t NATSTurns) Publish(ctx context.Context, msg platform.TurnMessage) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	_, err = t.JS.Publish(ctx, platform.SubjectAgentTurn+msg.SessionID, data, jetstream.WithMsgID(msg.MessageID))
	return err
}

// NATSEvents subscribes to the core NATS subject the agent publishes events to.
type NATSEvents struct{ NC *nats.Conn }

func (e NATSEvents) Subscribe(sessionID string, ch chan<- []byte) (func(), error) {
	sub, err := e.NC.Subscribe(platform.SessionEventsSubject(sessionID), func(m *nats.Msg) {
		select {
		case ch <- bytes.Clone(m.Data):
		default:
			// A client that does not read is dropped events rather than stalling NATS.
		}
	})
	if err != nil {
		return nil, err
	}
	return func() { _ = sub.Unsubscribe() }, nil
}
