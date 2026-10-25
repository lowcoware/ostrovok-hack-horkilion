package transfer

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	commonv1 "horkilion/gen/common/v1"
	transferv1 "horkilion/gen/transfer/v1"
	"horkilion/internal/dataset"
)

var bg = context.Background()

func provider(t *testing.T) (*Synthetic, *dataset.Data) {
	t.Helper()
	d, err := dataset.Load("../dataset/testdata/snapshot")
	if err != nil {
		t.Fatal(err)
	}
	return NewSynthetic(d), d
}

func find(t *testing.T, s *Synthetic, from, to string, pets bool) []*transferv1.Transfer {
	t.Helper()
	resp, err := s.SearchTransfers(bg, &transferv1.SearchTransfersRequest{
		From: &commonv1.Place{Id: from}, To: &commonv1.Place{Id: to}, DepartAt: "2026-11-20T12:45:00Z",
		Passengers: &commonv1.Travelers{Adults: 2, ChildAges: []int32{6}, Pets: pets},
	})
	if err != nil {
		t.Fatal(err)
	}
	return resp.GetTransfers()
}

func TestAirportToHotelOptionsMatchOffers(t *testing.T) {
	s, d := provider(t)
	hotel := d.HotelsByDest["sochi"][0].ID
	got := find(t, s, "AER", hotel, false)
	if len(got) < 2 {
		t.Fatalf("want at least two modes, got %d", len(got))
	}
	for _, x := range got {
		if x.GetDepartAt() != "2026-11-20T15:45:00+03:00" {
			t.Errorf("%s: depart_at must be in the local zone, got %s", x.GetId(), x.GetDepartAt())
		}
		offer, err := s.GetOffer(bg, &transferv1.GetOfferRequest{TransferId: x.GetId(), Passengers: &commonv1.Travelers{Adults: 2, ChildAges: []int32{6}}})
		if err != nil || offer.GetPriceRub() != x.GetPriceRub() || offer.GetDepartAt() != x.GetDepartAt() || offer.GetDurationMinutes() != x.GetDurationMinutes() {
			t.Errorf("%s: offer differs from search (%v)", x.GetId(), err)
		}
		if x.GetPriceRub() <= 0 || x.GetDurationMinutes() <= 0 {
			t.Errorf("%s misses price or duration", x.GetId())
		}
	}
}

func TestPetsMakeTaxiDearerAndDropShuttle(t *testing.T) {
	s, d := provider(t)
	hotel := d.HotelsByDest["sochi"][0].ID
	byMode := func(ts []*transferv1.Transfer) map[transferv1.Mode]int64 {
		m := map[transferv1.Mode]int64{}
		for _, x := range ts {
			m[x.GetMode()] = x.GetPriceRub()
		}
		return m
	}
	plain, pets := byMode(find(t, s, "AER", hotel, false)), byMode(find(t, s, "AER", hotel, true))
	if pets[transferv1.Mode_MODE_TAXI] <= plain[transferv1.Mode_MODE_TAXI] {
		t.Errorf("a taxi with a pet must cost more: %d vs %d", pets[transferv1.Mode_MODE_TAXI], plain[transferv1.Mode_MODE_TAXI])
	}
	if _, ok := pets[transferv1.Mode_MODE_SHUTTLE]; ok {
		t.Error("no shuttle with a pet")
	}
	if len(pets) < 2 {
		t.Error("still at least two modes with a pet")
	}
}

func TestStationAndBackToStation(t *testing.T) {
	s, d := provider(t)
	hotel := d.HotelsByDest["sochi"][0].ID
	if len(find(t, s, hotel, "sochi", false)) < 2 {
		t.Error("hotel to station must have at least two modes")
	}
}

func TestBadInput(t *testing.T) {
	s, d := provider(t)
	hotel := d.HotelsByDest["sochi"][0].ID
	party := &commonv1.Travelers{Adults: 1}
	for name, req := range map[string]*transferv1.SearchTransfersRequest{
		"unknown place": {From: &commonv1.Place{Id: "XXX"}, To: &commonv1.Place{Id: hotel}, DepartAt: "2026-11-20T12:45:00Z", Passengers: party},
		"two airports":  {From: &commonv1.Place{Id: "AER"}, To: &commonv1.Place{Id: "SVO"}, DepartAt: "2026-11-20T12:45:00Z", Passengers: party},
		"bad time":      {From: &commonv1.Place{Id: "AER"}, To: &commonv1.Place{Id: hotel}, DepartAt: "2026-11-20 12:45", Passengers: party},
		"no adults":     {From: &commonv1.Place{Id: "AER"}, To: &commonv1.Place{Id: hotel}, DepartAt: "2026-11-20T12:45:00Z", Passengers: &commonv1.Travelers{}},
	} {
		if _, err := s.SearchTransfers(bg, req); status.Code(err) != codes.InvalidArgument {
			t.Errorf("%s: want INVALID_ARGUMENT, got %v", name, err)
		}
	}
	for _, id := range []string{"", "taxi:AER:" + hotel + ":x", "taxi:AER:nowhere:1763642700", "boat:AER:" + hotel + ":1763642700"} {
		if _, err := s.GetOffer(bg, &transferv1.GetOfferRequest{TransferId: id, Passengers: party}); status.Code(err) != codes.NotFound {
			t.Errorf("%q: want NOT_FOUND, got %v", id, err)
		}
	}
}
