package booking

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	bookingv1 "horkilion/gen/booking/v1"
	commonv1 "horkilion/gen/common/v1"
	lodgingv1 "horkilion/gen/lodging/v1"
	transportv1 "horkilion/gen/transport/v1"
	"horkilion/internal/transport"
)

// fakeLodging prices every known hotel at a fixed total.
type fakeLodging struct {
	lodgingv1.LodgingServiceClient
	prices map[string]int64
}

func (f fakeLodging) GetOffer(_ context.Context, req *lodgingv1.GetOfferRequest, _ ...grpc.CallOption) (*lodgingv1.Hotel, error) {
	p, ok := f.prices[req.GetHotelId()]
	if !ok {
		return nil, status.Error(codes.NotFound, "unknown hotel id")
	}
	return &lodgingv1.Hotel{Id: req.GetHotelId(), TotalPriceRub: p}, nil
}

func stayItem(id string, price int64) *bookingv1.OrderItem {
	return &bookingv1.OrderItem{
		Kind: bookingv1.ItemKind_ITEM_KIND_STAY, OfferId: id, PriceRub: price, Title: id,
		Stay: &lodgingv1.Stay{CheckIn: "2026-11-07", Nights: 3, Travelers: &commonv1.Travelers{Adults: 2}},
	}
}

func TestRepriceAcceptsSeenPrices(t *testing.T) {
	s := &Server{suppliers: Suppliers{Lodging: fakeLodging{prices: map[string]int64{"a": 30000, "b": 12000}}}}
	total, err := s.reprice(context.Background(), []*bookingv1.OrderItem{stayItem("a", 30000), stayItem("b", 12000)})
	if err != nil || total != 42000 {
		t.Fatalf("got total %d, err %v", total, err)
	}
}

func TestRepriceRejectsChangedPrice(t *testing.T) {
	s := &Server{suppliers: Suppliers{Lodging: fakeLodging{prices: map[string]int64{"a": 30000, "b": 13000}}}}
	_, err := s.reprice(context.Background(), []*bookingv1.OrderItem{stayItem("a", 30000), stayItem("b", 12000)})
	st := status.Convert(err)
	if st.Code() != codes.FailedPrecondition {
		t.Fatalf("want FAILED_PRECONDITION, got %v", err)
	}
	var changed *bookingv1.PriceChanged
	for _, d := range st.Details() {
		if pc, ok := d.(*bookingv1.PriceChanged); ok {
			changed = pc
		}
	}
	if changed == nil || len(changed.GetItems()) != 1 {
		t.Fatalf("want one changed item in the details, got %v", st.Details())
	}
	if it := changed.GetItems()[0]; it.GetIndex() != 1 || it.GetSeenPriceRub() != 12000 || it.GetActualPriceRub() != 13000 {
		t.Errorf("wrong changed item: %v", it)
	}
}

func TestRepriceRejectsUnavailableOffer(t *testing.T) {
	s := &Server{suppliers: Suppliers{Lodging: fakeLodging{prices: map[string]int64{}}}}
	_, err := s.reprice(context.Background(), []*bookingv1.OrderItem{stayItem("gone", 30000)})
	if status.Code(err) != codes.FailedPrecondition {
		t.Errorf("an offer that disappeared must be FAILED_PRECONDITION, got %v", err)
	}
}

func TestRepriceStubSupplierIsUnimplemented(t *testing.T) {
	s := &Server{suppliers: Suppliers{Transport: stubTransport{}}}
	item := &bookingv1.OrderItem{Kind: bookingv1.ItemKind_ITEM_KIND_JOURNEY, OfferId: "j", PriceRub: 1, Travelers: &commonv1.Travelers{Adults: 1}}
	if _, err := s.reprice(context.Background(), []*bookingv1.OrderItem{item}); status.Code(err) != codes.Unimplemented {
		t.Errorf("want UNIMPLEMENTED from the stub transport, got %v", err)
	}
}

func TestValidate(t *testing.T) {
	ok := &bookingv1.CreateOrderRequest{
		IdempotencyKey: "4b0c5a5e-8f0e-4a43-9c55-2f3b9e3c7a10", SessionId: "4b0c5a5e-8f0e-4a43-9c55-2f3b9e3c7a10",
		GuestName: "Иван", Items: []*bookingv1.OrderItem{stayItem("a", 1)},
	}
	if err := validate(ok); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
	noStay := stayItem("a", 1)
	noStay.Stay = nil
	for name, req := range map[string]*bookingv1.CreateOrderRequest{
		"no items":   {IdempotencyKey: ok.IdempotencyKey, SessionId: ok.SessionId, GuestName: "Иван"},
		"zero price": {IdempotencyKey: ok.IdempotencyKey, SessionId: ok.SessionId, GuestName: "Иван", Items: []*bookingv1.OrderItem{stayItem("a", 0)}},
		"no stay":    {IdempotencyKey: ok.IdempotencyKey, SessionId: ok.SessionId, GuestName: "Иван", Items: []*bookingv1.OrderItem{noStay}},
	} {
		if validate(req) == nil {
			t.Errorf("%s must be rejected", name)
		}
	}
}

// stubTransport calls the real stub provider in-process.
type stubTransport struct {
	transportv1.TransportServiceClient
}

func (stubTransport) GetOffer(ctx context.Context, req *transportv1.GetOfferRequest, _ ...grpc.CallOption) (*transportv1.Journey, error) {
	return transport.Stub{}.GetOffer(ctx, req)
}
