// Package booking implements BookingService: idempotent orders of several items
// with simulated payment and a transactional outbox.
package booking

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"

	bookingv1 "horkilion/gen/booking/v1"
	lodgingv1 "horkilion/gen/lodging/v1"
	transferv1 "horkilion/gen/transfer/v1"
	transportv1 "horkilion/gen/transport/v1"
	"horkilion/internal/platform"
)

// impulse: payment is simulated and every order is created as paid, plug in a payment provider sandbox when the demo needs a payment step.
const statusPaid = "paid"

const maxItems = 10

// Suppliers re-price order items. Each is the client of one supplier contract.
type Suppliers struct {
	Lodging   lodgingv1.LodgingServiceClient
	Transport transportv1.TransportServiceClient
	Transfer  transferv1.TransferServiceClient
}

// Server implements bookingv1.BookingServiceServer.
type Server struct {
	bookingv1.UnimplementedBookingServiceServer
	db        *pgxpool.Pool
	suppliers Suppliers
}

func NewServer(db *pgxpool.Pool, suppliers Suppliers) *Server {
	return &Server{db: db, suppliers: suppliers}
}

const orderColumns = `id, session_id, status, items, total_rub, guest_name, created_at`

func (s *Server) CreateOrder(ctx context.Context, req *bookingv1.CreateOrderRequest) (*bookingv1.Order, error) {
	owner := platform.OwnerFrom(ctx)
	if owner == "" {
		return nil, status.Error(codes.Unauthenticated, "x-owner-id metadata is required")
	}
	if err := validate(req); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	if order, err := s.findByKey(ctx, owner, req.GetIdempotencyKey()); err == nil {
		return order, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, status.Error(codes.Internal, err.Error())
	}

	// The price always comes from the supplier, never from the client: the
	// price the user saw is only compared with it.
	total, err := s.reprice(ctx, req.GetItems())
	if err != nil {
		return nil, err
	}

	order, err := s.insert(ctx, owner, req, total)
	if errors.Is(err, pgx.ErrNoRows) {
		// A concurrent request with the same key won the insert.
		order, err = s.findByKey(ctx, owner, req.GetIdempotencyKey())
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, status.Error(codes.InvalidArgument, "idempotency_key is already used")
		}
	}
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return order, nil
}

// reprice asks each item's supplier for the current price. When any price
// differs from what the user saw, it fails with FAILED_PRECONDITION and a
// PriceChanged detail listing every changed item.
func (s *Server) reprice(ctx context.Context, items []*bookingv1.OrderItem) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var (
		total   int64
		changed []*bookingv1.ItemPrice
	)
	for i, item := range items {
		actual, err := s.price(ctx, item)
		if status.Code(err) == codes.NotFound {
			return 0, status.Errorf(codes.FailedPrecondition, "item %d (%s) is no longer offered", i, item.GetOfferId())
		}
		if err != nil {
			return 0, err
		}
		if actual != item.GetPriceRub() {
			changed = append(changed, &bookingv1.ItemPrice{
				Index: int32(i), Kind: item.GetKind(), OfferId: item.GetOfferId(),
				SeenPriceRub: item.GetPriceRub(), ActualPriceRub: actual,
			})
		}
		total += actual
	}
	if len(changed) > 0 {
		st, err := status.New(codes.FailedPrecondition, "prices changed").WithDetails(&bookingv1.PriceChanged{Items: changed})
		if err != nil {
			return 0, status.Error(codes.Internal, err.Error())
		}
		return 0, st.Err()
	}
	return total, nil
}

func (s *Server) price(ctx context.Context, item *bookingv1.OrderItem) (int64, error) {
	switch item.GetKind() {
	case bookingv1.ItemKind_ITEM_KIND_STAY:
		h, err := s.suppliers.Lodging.GetOffer(ctx, &lodgingv1.GetOfferRequest{HotelId: item.GetOfferId(), Stay: item.GetStay()})
		return h.GetTotalPriceRub(), err
	case bookingv1.ItemKind_ITEM_KIND_JOURNEY:
		j, err := s.suppliers.Transport.GetOffer(ctx, &transportv1.GetOfferRequest{JourneyId: item.GetOfferId(), Passengers: item.GetTravelers()})
		return j.GetPriceRub(), err
	case bookingv1.ItemKind_ITEM_KIND_TRANSFER:
		t, err := s.suppliers.Transfer.GetOffer(ctx, &transferv1.GetOfferRequest{TransferId: item.GetOfferId(), Passengers: item.GetTravelers()})
		return t.GetPriceRub(), err
	}
	return 0, status.Errorf(codes.InvalidArgument, "unknown item kind %s", item.GetKind())
}

func (s *Server) GetOrder(ctx context.Context, req *bookingv1.GetOrderRequest) (*bookingv1.Order, error) {
	owner := platform.OwnerFrom(ctx)
	id, err := uuid.Parse(req.GetId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "id must be a UUID")
	}
	order, err := scanOrder(s.db.QueryRow(ctx,
		`SELECT `+orderColumns+` FROM booking.orders WHERE id = $1 AND owner_id = $2`, id, owner))
	if errors.Is(err, pgx.ErrNoRows) || owner == "" {
		// Another owner's order looks the same as a missing one.
		return nil, status.Error(codes.NotFound, "order not found")
	}
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return order, nil
}

func (s *Server) ListOrders(ctx context.Context, _ *bookingv1.ListOrdersRequest) (*bookingv1.ListOrdersResponse, error) {
	owner := platform.OwnerFrom(ctx)
	if owner == "" {
		return nil, status.Error(codes.Unauthenticated, "x-owner-id metadata is required")
	}
	// impulse: no pagination, add a cursor when an owner has more than a hundred orders.
	rows, err := s.db.Query(ctx,
		`SELECT `+orderColumns+` FROM booking.orders WHERE owner_id = $1 ORDER BY created_at DESC LIMIT 100`, owner)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	orders, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (*bookingv1.Order, error) { return scanOrder(row) })
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &bookingv1.ListOrdersResponse{Orders: orders}, nil
}

func (s *Server) insert(ctx context.Context, owner string, req *bookingv1.CreateOrderRequest, total int64) (*bookingv1.Order, error) {
	items, err := marshalItems(req.GetItems())
	if err != nil {
		return nil, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	order, err := scanOrder(tx.QueryRow(ctx, `
		INSERT INTO booking.orders (idempotency_key, owner_id, session_id, guest_name, items, total_rub, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (idempotency_key) DO NOTHING
		RETURNING `+orderColumns,
		req.GetIdempotencyKey(), owner, req.GetSessionId(), strings.TrimSpace(req.GetGuestName()), items, total, statusPaid,
	))
	if err != nil {
		return nil, err
	}

	payload, err := json.Marshal(bookingCreated(owner, order))
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO booking.outbox (subject, payload) VALUES ($1, $2)`, platform.SubjectBookingCreated, payload); err != nil {
		return nil, err
	}
	return order, tx.Commit(ctx)
}

func bookingCreated(owner string, o *bookingv1.Order) platform.BookingCreated {
	ev := platform.BookingCreated{ID: o.GetId(), SessionID: o.GetSessionId(), OwnerID: owner, TotalRub: o.GetTotalRub()}
	for _, it := range o.GetItems() {
		ev.Items = append(ev.Items, platform.BookingItemInfo{
			Kind: strings.ToLower(strings.TrimPrefix(it.GetKind().String(), "ITEM_KIND_")), Title: it.GetTitle(), PriceRub: it.GetPriceRub(),
		})
	}
	return ev
}

func (s *Server) findByKey(ctx context.Context, owner, key string) (*bookingv1.Order, error) {
	return scanOrder(s.db.QueryRow(ctx,
		`SELECT `+orderColumns+` FROM booking.orders WHERE idempotency_key = $1 AND owner_id = $2`, key, owner))
}

func marshalItems(items []*bookingv1.OrderItem) ([]byte, error) {
	raw := make([]json.RawMessage, 0, len(items))
	for _, it := range items {
		b, err := protojson.Marshal(it)
		if err != nil {
			return nil, err
		}
		raw = append(raw, b)
	}
	return json.Marshal(raw)
}

func unmarshalItems(data []byte) ([]*bookingv1.OrderItem, error) {
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	items := make([]*bookingv1.OrderItem, 0, len(raw))
	for _, r := range raw {
		it := &bookingv1.OrderItem{}
		if err := protojson.Unmarshal(r, it); err != nil {
			return nil, err
		}
		items = append(items, it)
	}
	return items, nil
}

func scanOrder(row pgx.Row) (*bookingv1.Order, error) {
	var (
		o         bookingv1.Order
		id, sid   uuid.UUID
		items     []byte
		createdAt time.Time
	)
	if err := row.Scan(&id, &sid, &o.Status, &items, &o.TotalRub, &o.GuestName, &createdAt); err != nil {
		return nil, err
	}
	parsed, err := unmarshalItems(items)
	if err != nil {
		return nil, fmt.Errorf("order %s items: %w", id, err)
	}
	o.Id = id.String()
	o.SessionId = sid.String()
	o.Items = parsed
	o.CreatedAt = createdAt.UTC().Format(time.RFC3339)
	return &o, nil
}

func validate(req *bookingv1.CreateOrderRequest) error {
	if n := len(req.GetIdempotencyKey()); n < 8 || n > 100 {
		return errors.New("idempotency_key must be 8 to 100 characters")
	}
	if _, err := uuid.Parse(req.GetSessionId()); err != nil {
		return errors.New("session_id must be a UUID")
	}
	if n := len([]rune(strings.TrimSpace(req.GetGuestName()))); n < 1 || n > 200 {
		return errors.New("guest_name must be 1 to 200 characters")
	}
	items := req.GetItems()
	if len(items) == 0 || len(items) > maxItems {
		return fmt.Errorf("an order must have 1 to %d items", maxItems)
	}
	for i, it := range items {
		switch {
		case it.GetOfferId() == "":
			return fmt.Errorf("items[%d].offer_id is required", i)
		case it.GetPriceRub() <= 0:
			return fmt.Errorf("items[%d].price_rub must be positive", i)
		case it.GetKind() == bookingv1.ItemKind_ITEM_KIND_STAY && it.GetStay() == nil:
			return fmt.Errorf("items[%d].stay is required for a stay", i)
		case it.GetKind() != bookingv1.ItemKind_ITEM_KIND_STAY && it.GetTravelers() == nil:
			return fmt.Errorf("items[%d].travelers is required", i)
		}
	}
	return nil
}
