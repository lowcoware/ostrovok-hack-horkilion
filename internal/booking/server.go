// Package booking implements BookingService: idempotent orders with simulated
// payment and a transactional outbox.
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

	bookingv1 "horkilion/gen/booking/v1"
	inventoryv1 "horkilion/gen/inventory/v1"
	"horkilion/internal/platform"
)

// impulse: payment is simulated and every order is created as paid, plug in a payment provider sandbox when the demo needs a payment step.
const statusPaid = "paid"

// Server implements bookingv1.BookingServiceServer.
type Server struct {
	bookingv1.UnimplementedBookingServiceServer
	db        *pgxpool.Pool
	inventory inventoryv1.InventoryServiceClient
}

func NewServer(db *pgxpool.Pool, inventory inventoryv1.InventoryServiceClient) *Server {
	return &Server{db: db, inventory: inventory}
}

const orderColumns = `id, session_id, status, hotel_id, hotel_name, check_in, nights, total_price_rub, created_at`

func (s *Server) CreateOrder(ctx context.Context, req *bookingv1.CreateOrderRequest) (*bookingv1.Order, error) {
	if err := validate(req); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	if order, err := s.findByKey(ctx, req.GetIdempotencyKey()); err == nil {
		return order, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, status.Error(codes.Internal, err.Error())
	}

	// The price always comes from inventory, never from the client.
	offerCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	offer, err := s.inventory.GetOffer(offerCtx, &inventoryv1.GetOfferRequest{
		HotelId: req.GetHotelId(),
		Stay: &inventoryv1.Stay{
			CheckIn:  req.GetCheckIn(),
			Nights:   req.GetNights(),
			Adults:   req.GetAdults(),
			Children: req.GetChildren(),
			WithPets: req.GetWithPets(),
		},
	})
	if err != nil {
		return nil, err
	}

	order, err := s.insert(ctx, req, offer)
	if errors.Is(err, pgx.ErrNoRows) {
		// A concurrent request with the same key won the insert.
		order, err = s.findByKey(ctx, req.GetIdempotencyKey())
	}
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return order, nil
}

func (s *Server) GetOrder(ctx context.Context, req *bookingv1.GetOrderRequest) (*bookingv1.Order, error) {
	id, err := uuid.Parse(req.GetId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "id must be a UUID")
	}
	order, err := scanOrder(s.db.QueryRow(ctx, `SELECT `+orderColumns+` FROM booking.orders WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "order not found")
	}
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return order, nil
}

func (s *Server) insert(ctx context.Context, req *bookingv1.CreateOrderRequest, offer *inventoryv1.Hotel) (*bookingv1.Order, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	order, err := scanOrder(tx.QueryRow(ctx, `
		INSERT INTO booking.orders
			(idempotency_key, session_id, hotel_id, hotel_name, check_in, nights, adults, children, guest_name, total_price_rub, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (idempotency_key) DO NOTHING
		RETURNING `+orderColumns,
		req.GetIdempotencyKey(), req.GetSessionId(), offer.GetId(), offer.GetName(), req.GetCheckIn(),
		req.GetNights(), req.GetAdults(), req.GetChildren(), strings.TrimSpace(req.GetGuestName()),
		offer.GetTotalPriceRub(), statusPaid,
	))
	if err != nil {
		return nil, err
	}

	payload, err := json.Marshal(order)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO booking.outbox (subject, payload) VALUES ($1, $2)`, platform.SubjectBookingCreated, payload); err != nil {
		return nil, err
	}
	return order, tx.Commit(ctx)
}

func (s *Server) findByKey(ctx context.Context, key string) (*bookingv1.Order, error) {
	return scanOrder(s.db.QueryRow(ctx, `SELECT `+orderColumns+` FROM booking.orders WHERE idempotency_key = $1`, key))
}

func scanOrder(row pgx.Row) (*bookingv1.Order, error) {
	var (
		o         bookingv1.Order
		id, sid   uuid.UUID
		checkIn   time.Time
		createdAt time.Time
	)
	if err := row.Scan(&id, &sid, &o.Status, &o.HotelId, &o.HotelName, &checkIn, &o.Nights, &o.TotalPriceRub, &createdAt); err != nil {
		return nil, err
	}
	o.Id = id.String()
	o.SessionId = sid.String()
	o.CheckIn = checkIn.Format(time.DateOnly)
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
	if _, err := time.Parse(time.DateOnly, req.GetCheckIn()); err != nil {
		return fmt.Errorf("check_in must be YYYY-MM-DD")
	}
	return nil
}
