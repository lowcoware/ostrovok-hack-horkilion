package lodging

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	commonv1 "horkilion/gen/common/v1"
	lodgingv1 "horkilion/gen/lodging/v1"
	"horkilion/internal/dataset"
)

var bg = context.Background()

func osmProvider(t *testing.T) (*OSM, *dataset.Data) {
	t.Helper()
	d, err := dataset.Load("../dataset/testdata/snapshot")
	if err != nil {
		t.Fatal(err)
	}
	return NewOSM(d), d
}

func stayIn(nights int32, adults int32, pets bool, ages ...int32) *lodgingv1.Stay {
	return &lodgingv1.Stay{CheckIn: "2026-11-20", Nights: nights, Travelers: &commonv1.Travelers{Adults: adults, ChildAges: ages, Pets: pets}}
}

func TestOSMSearchMatchesOffer(t *testing.T) {
	s, _ := osmProvider(t)
	resp, err := s.SearchHotels(bg, &lodgingv1.SearchHotelsRequest{City: "Сочи", Stay: stayIn(3, 2, false, 6)})
	if err != nil || len(resp.GetHotels()) == 0 {
		t.Fatalf("Сочи must have hotels: %v", err)
	}
	for _, h := range resp.GetHotels() {
		offer, err := s.GetOffer(bg, &lodgingv1.GetOfferRequest{HotelId: h.GetId(), Stay: stayIn(3, 2, false, 6)})
		if err != nil || offer.GetTotalPriceRub() != h.GetTotalPriceRub() {
			t.Errorf("%s: offer %d differs from search %d (%v)", h.GetName(), offer.GetTotalPriceRub(), h.GetTotalPriceRub(), err)
		}
		if len(h.GetSources()) < 2 || h.GetPlace().GetPoint() == nil {
			t.Errorf("%s needs OSM and synthetic sources and a point", h.GetName())
		}
	}
}

func TestOSMTouristTaxIsInTheTotal(t *testing.T) {
	s, d := osmProvider(t)
	h := d.HotelsByDest["sochi"][0]
	withTax, _ := s.GetOffer(bg, &lodgingv1.GetOfferRequest{HotelId: h.ID, Stay: stayIn(3, 2, false)})
	delete(d.Calibration.TouristTaxRubPerNight, "sochi")
	noTax, _ := s.GetOffer(bg, &lodgingv1.GetOfferRequest{HotelId: h.ID, Stay: stayIn(3, 2, false)})
	if withTax.GetTotalPriceRub()-noTax.GetTotalPriceRub() != 600 {
		t.Errorf("tax of 100 per adult per night for 2 adults and 3 nights must add 600, got %d", withTax.GetTotalPriceRub()-noTax.GetTotalPriceRub())
	}
}

func TestOSMPetsAndUnknowns(t *testing.T) {
	s, _ := osmProvider(t)
	resp, _ := s.SearchHotels(bg, &lodgingv1.SearchHotelsRequest{City: "Сочи", Stay: stayIn(2, 1, true)})
	if len(resp.GetHotels()) == 0 {
		t.Fatal("the test snapshot must have a pet-friendly hotel in Сочи")
	}
	for _, h := range resp.GetHotels() {
		if !h.GetPetsAllowed() {
			t.Errorf("%s does not accept pets but was returned", h.GetName())
		}
	}
	if resp, err := s.SearchHotels(bg, &lodgingv1.SearchHotelsRequest{City: "Атлантида", Stay: stayIn(2, 1, false)}); err != nil || len(resp.GetHotels()) != 0 {
		t.Errorf("an unknown city is an empty answer, got %v, %v", resp, err)
	}
	if _, err := s.GetOffer(bg, &lodgingv1.GetOfferRequest{HotelId: "osm.node.0", Stay: stayIn(2, 1, false)}); status.Code(err) != codes.NotFound {
		t.Errorf("unknown hotel must be NOT_FOUND, got %v", err)
	}
}

func TestOSMFindsDestinationByEnglishName(t *testing.T) {
	s, _ := osmProvider(t)
	resp, _ := s.SearchHotels(bg, &lodgingv1.SearchHotelsRequest{City: "Antalya", Stay: stayIn(2, 2, false)})
	if len(resp.GetHotels()) == 0 {
		t.Error("Antalya must find the hotels of the Анталия destination")
	}
}
