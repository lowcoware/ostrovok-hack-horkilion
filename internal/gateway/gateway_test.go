package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	bookingv1 "horkilion/gen/booking/v1"
	commonv1 "horkilion/gen/common/v1"
	lodgingv1 "horkilion/gen/lodging/v1"
	"horkilion/internal/agent/plan"
	"horkilion/internal/apitest"
	"horkilion/internal/platform"
)

// ---------------------------------------------------------------- fakes

type fakeStore struct {
	mu     sync.Mutex
	owners map[string]string
	plans  map[string]plan.TripPlan
}

func newFakeStore() *fakeStore {
	return &fakeStore{owners: map[string]string{}, plans: map[string]plan.TripPlan{}}
}

func (s *fakeStore) CreateSession(_ context.Context, owner string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := uuid.NewString()
	s.owners[id] = owner
	return id, nil
}

func (s *fakeStore) CheckOwner(_ context.Context, id, owner string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.owners[id] != owner {
		return errNotFound
	}
	return nil
}

func (s *fakeStore) ListSessions(_ context.Context, owner string) ([]SessionSummary, error) {
	return nil, nil
}

func (s *fakeStore) LoadPlan(_ context.Context, id string) (plan.TripPlan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.plans[id]; ok {
		return p, nil
	}
	return plan.New(id), nil
}

func (s *fakeStore) Messages(context.Context, string) ([]ChatMessage, error) {
	return []ChatMessage{{Role: "user", Text: "Хочу в Сочи"}}, nil
}

type fakeTurns struct{ got []platform.TurnMessage }

func (t *fakeTurns) Publish(_ context.Context, msg platform.TurnMessage) error {
	t.got = append(t.got, msg)
	return nil
}

type fakeEvents struct{}

func (fakeEvents) Subscribe(string, chan<- []byte) (func(), error) { return func() {}, nil }

// fakeBooking checks the owner metadata and prices every stay at priceRub.
type fakeBooking struct {
	bookingv1.BookingServiceClient
	priceRub int64
	owners   []string
}

func (b *fakeBooking) owner(ctx context.Context) string {
	md, _ := metadata.FromOutgoingContext(ctx)
	v := md.Get(platform.OwnerMetadataKey)
	if len(v) == 0 {
		return ""
	}
	b.owners = append(b.owners, v[0])
	return v[0]
}

func (b *fakeBooking) CreateOrder(ctx context.Context, req *bookingv1.CreateOrderRequest, _ ...grpc.CallOption) (*bookingv1.Order, error) {
	if b.owner(ctx) == "" {
		return nil, status.Error(codes.Unauthenticated, "no owner")
	}
	if it := req.GetItems()[0]; it.GetPriceRub() != b.priceRub {
		st, _ := status.New(codes.FailedPrecondition, "prices changed").WithDetails(&bookingv1.PriceChanged{Items: []*bookingv1.ItemPrice{{
			Index: 0, Kind: it.GetKind(), OfferId: it.GetOfferId(), SeenPriceRub: it.GetPriceRub(), ActualPriceRub: b.priceRub,
		}}})
		return nil, st.Err()
	}
	return testOrder(req.GetSessionId(), req.GetItems()), nil
}

func (b *fakeBooking) GetOrder(ctx context.Context, _ *bookingv1.GetOrderRequest, _ ...grpc.CallOption) (*bookingv1.Order, error) {
	b.owner(ctx)
	return nil, status.Error(codes.NotFound, "order not found")
}

func (b *fakeBooking) ListOrders(ctx context.Context, _ *bookingv1.ListOrdersRequest, _ ...grpc.CallOption) (*bookingv1.ListOrdersResponse, error) {
	b.owner(ctx)
	return &bookingv1.ListOrdersResponse{Orders: []*bookingv1.Order{testOrder(uuid.NewString(), nil)}}, nil
}

func testOrder(sessionID string, items []*bookingv1.OrderItem) *bookingv1.Order {
	var total int64
	for _, it := range items {
		total += it.GetPriceRub()
	}
	return &bookingv1.Order{
		Id: uuid.NewString(), SessionId: sessionID, Status: "paid", Items: items, TotalRub: total,
		GuestName: "Иван", CreatedAt: "2026-10-25T12:00:00Z",
	}
}

// ---------------------------------------------------------------- harness

type client struct {
	t      *testing.T
	router http.Handler
	cookie *http.Cookie
}

func (c *client) do(method, path, body string, header map[string]string) *httptest.ResponseRecorder {
	c.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	if c.cookie != nil {
		req.AddCookie(c.cookie)
	}
	rec := httptest.NewRecorder()
	c.router.ServeHTTP(rec, req)
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == ownerCookie {
			c.cookie = ck
		}
	}
	apitest.ValidateResponse(c.t, httptest.NewRequest(method, path, nil), rec)
	return rec
}

func (c *client) newSession() string {
	c.t.Helper()
	rec := c.do(http.MethodPost, "/api/v1/sessions", "", nil)
	if rec.Code != http.StatusCreated {
		c.t.Fatalf("create session: %d %s", rec.Code, rec.Body)
	}
	var body struct {
		SessionID string `json:"session_id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return body.SessionID
}

func setup(t *testing.T) (*fakeStore, *fakeTurns, *fakeBooking, func() *client) {
	store, turns, booking := newFakeStore(), &fakeTurns{}, &fakeBooking{priceRub: 30000}
	router := New(store, turns, fakeEvents{}, booking).Router()
	return store, turns, booking, func() *client { return &client{t: t, router: router} }
}

var today = time.Now().UTC().Truncate(24 * time.Hour)

// searchedPlan is a plan with two hotels, the first one selected.
func searchedPlan(id string) plan.TripPlan {
	p := plan.New(id)
	city := "Сочи"
	p.Apply(plan.Patch{Destination: &city}, today)
	p.FillDefaults(today)
	var opts []plan.Option
	for i, price := range []int64{30000, 45000} {
		hid := []string{"syn.a.0", "syn.a.1"}[i]
		opts = append(opts, plan.StayOption(&lodgingv1.Hotel{
			Id: hid, Name: "Отель " + hid, City: city, Stars: 4, Rating: 8.1, Meal: "breakfast",
			Place: &commonv1.Place{Name: "Отель", Kind: commonv1.Place_KIND_HOTEL}, CheckInTime: "14:00", CheckOutTime: "12:00",
			CancellationPolicy: "бесплатная отмена", PricePerNightRub: price / 3, TotalPriceRub: price,
			Sources: []*commonv1.Source{{Provider: "synthetic", Kind: commonv1.Source_KIND_SYNTHETIC, FieldScope: "*"}},
		}))
	}
	p.SetOptions(plan.Stay, opts)
	p.Apply(plan.Patch{Select: &plan.Selection{Component: plan.Stay, OptionID: "syn.a.0"}}, today)
	p.NextStage()
	return p
}

// ---------------------------------------------------------------- tests

func TestSessionLifecycleMatchesContract(t *testing.T) {
	store, turns, _, newClient := setup(t)
	c := newClient()
	id := c.newSession()
	if c.cookie == nil || !c.cookie.HttpOnly {
		t.Fatal("the first request must set an HttpOnly owner cookie")
	}

	if rec := c.do(http.MethodGet, "/api/v1/sessions/"+id, "", nil); rec.Code != http.StatusOK {
		t.Fatalf("get empty session: %d %s", rec.Code, rec.Body)
	}
	store.plans[id] = searchedPlan(id)
	if rec := c.do(http.MethodGet, "/api/v1/sessions/"+id, "", nil); rec.Code != http.StatusOK {
		t.Fatalf("get session with a plan: %d %s", rec.Code, rec.Body)
	}

	rec := c.do(http.MethodPost, "/api/v1/sessions/"+id+"/messages", `{"text":"Хочу в Сочи"}`, nil)
	if rec.Code != http.StatusAccepted || len(turns.got) != 1 || turns.got[0].Text != "Хочу в Сочи" || turns.got[0].OwnerID == "" {
		t.Fatalf("post message: %d %s, turns %+v", rec.Code, rec.Body, turns.got)
	}
	if rec := c.do(http.MethodPost, "/api/v1/sessions/"+id+"/messages", `{"text":"  "}`, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("empty text must be 400, got %d", rec.Code)
	}

	rec = c.do(http.MethodPost, "/api/v1/sessions/"+id+"/plan", `{"dates":{"nights":5},"select":{"component":"stay","option_id":"syn.a.1"}}`, nil)
	if rec.Code != http.StatusAccepted || len(turns.got) != 2 || len(turns.got[1].Patch) == 0 {
		t.Fatalf("patch plan: %d %s", rec.Code, rec.Body)
	}
	for _, bad := range []string{`{"nights":5}`, `{}`, `{"adults":0}`, `not json`} {
		if rec := c.do(http.MethodPost, "/api/v1/sessions/"+id+"/plan", bad, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("patch %s must be 400, got %d", bad, rec.Code)
		}
	}

	if rec := c.do(http.MethodGet, "/api/v1/sessions/not-a-uuid", "", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("malformed id must be 400, got %d", rec.Code)
	}
	if rec := c.do(http.MethodGet, "/api/v1/sessions", "", nil); rec.Code != http.StatusOK {
		t.Errorf("list sessions: %d", rec.Code)
	}
}

func TestForeignSessionIsNotFound(t *testing.T) {
	_, turns, _, newClient := setup(t)
	alice, bob := newClient(), newClient()
	id := alice.newSession()
	bob.newSession()

	for _, r := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/sessions/" + id, ""},
		{http.MethodPost, "/api/v1/sessions/" + id + "/messages", `{"text":"привет"}`},
		{http.MethodPost, "/api/v1/sessions/" + id + "/plan", `{"pets":true}`},
		{http.MethodGet, "/api/v1/sessions/" + id + "/events", ""},
		{http.MethodPost, "/api/v1/sessions/" + id + "/checkout", `{"guest_name":"Боб"}`},
		{http.MethodGet, "/api/v1/sessions/" + uuid.NewString(), ""},
	} {
		rec := bob.do(r.method, r.path, r.body, map[string]string{"Idempotency-Key": uuid.NewString()})
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s by another owner: want 404, got %d", r.method, r.path, rec.Code)
		}
	}
	if len(turns.got) != 0 {
		t.Errorf("no turn may be published for a foreign session, got %d", len(turns.got))
	}
}

func TestCheckoutMatchesContract(t *testing.T) {
	store, _, booking, newClient := setup(t)
	c := newClient()
	id := c.newSession()
	key := map[string]string{"Idempotency-Key": uuid.NewString()}

	if rec := c.do(http.MethodPost, "/api/v1/sessions/"+id+"/checkout", `{"guest_name":"Иван"}`, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("missing Idempotency-Key must be 400, got %d", rec.Code)
	}
	rec := c.do(http.MethodPost, "/api/v1/sessions/"+id+"/checkout", `{"guest_name":"Иван"}`, key)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "nothing_selected") {
		t.Fatalf("empty cart must be 409 nothing_selected, got %d %s", rec.Code, rec.Body)
	}

	store.plans[id] = searchedPlan(id)
	rec = c.do(http.MethodPost, "/api/v1/sessions/"+id+"/checkout", `{"guest_name":"Иван"}`, key)
	if rec.Code != http.StatusOK {
		t.Fatalf("checkout: %d %s", rec.Code, rec.Body)
	}
	var order struct {
		TotalRub int64 `json:"total_rub"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &order)
	if order.TotalRub != store.plans[id].TotalRub {
		t.Errorf("order total %d must equal the plan total %d", order.TotalRub, store.plans[id].TotalRub)
	}
	if len(booking.owners) == 0 || booking.owners[0] != "anon:"+c.cookie.Value {
		t.Errorf("booking must get the owner in metadata, got %v", booking.owners)
	}

	booking.priceRub = 31000
	rec = c.do(http.MethodPost, "/api/v1/sessions/"+id+"/checkout", `{"guest_name":"Иван"}`, map[string]string{"Idempotency-Key": uuid.NewString()})
	if rec.Code != http.StatusConflict {
		t.Fatalf("changed price must be 409, got %d %s", rec.Code, rec.Body)
	}
	var e struct {
		Error struct {
			Code    string `json:"code"`
			Details any    `json:"details"`
		} `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &e)
	if e.Error.Code != "price_changed" {
		t.Errorf("want price_changed, got %s", rec.Body)
	}
	apitest.ValidateSchema(t, "PriceChangedDetails", e.Error.Details)
}

func TestOtherEndpointsMatchContract(t *testing.T) {
	_, _, _, newClient := setup(t)
	c := newClient()
	for _, r := range []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/api/v1/me", http.StatusOK},
		{http.MethodGet, "/api/v1/orders", http.StatusOK},
		{http.MethodGet, "/api/v1/orders/" + uuid.NewString(), http.StatusNotFound},
		{http.MethodGet, "/api/v1/orders/42", http.StatusBadRequest},
		{http.MethodGet, "/api/v1/sources", http.StatusOK},
		{http.MethodPost, "/api/v1/auth/login", http.StatusNotImplemented},
	} {
		body := ""
		if r.method == http.MethodPost {
			body = `{"login":"ivan","password":"12345678"}`
		}
		if rec := c.do(r.method, r.path, body, nil); rec.Code != r.want {
			t.Errorf("%s %s: want %d, got %d %s", r.method, r.path, r.want, rec.Code, rec.Body)
		}
	}
}
