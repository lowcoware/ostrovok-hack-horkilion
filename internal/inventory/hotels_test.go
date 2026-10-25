package inventory

import (
	"testing"
	"time"
)

func TestOfferMatchesSearchPrice(t *testing.T) {
	stay := Stay{CheckIn: time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC), Nights: 3, Adults: 2}
	hotels := SearchHotels("Сочи", stay)
	if len(hotels) == 0 {
		t.Fatal("search returned no hotels")
	}
	for _, h := range hotels {
		offer, err := Offer(h.Id, stay)
		if err != nil {
			t.Fatalf("Offer(%s): %v", h.Id, err)
		}
		if offer.TotalPriceRub != h.TotalPriceRub || offer.Name != h.Name {
			t.Errorf("offer %s differs from search: %d vs %d", h.Id, offer.TotalPriceRub, h.TotalPriceRub)
		}
		if h.TotalPriceRub != h.PricePerNightRub*3 || h.TotalPriceRub <= 0 {
			t.Errorf("bad total for %s: %d", h.Id, h.TotalPriceRub)
		}
	}
}

func TestPetsFilter(t *testing.T) {
	stay := Stay{CheckIn: time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC), Nights: 1, Adults: 1, WithPets: true}
	for _, h := range SearchHotels("Казань", stay) {
		if !h.PetsAllowed {
			t.Errorf("%s does not allow pets but was returned", h.Name)
		}
	}
}

func TestOfferRejectsForeignIDs(t *testing.T) {
	stay := Stay{CheckIn: time.Now(), Nights: 1, Adults: 1}
	for _, id := range []string{"", "x", "syn.!!.1", "syn.fake.1", "syn..1", "syn.0KHQvtGH0Lg.99", "syn.0KHQvtGH0Lg.-1"} {
		if _, err := Offer(id, stay); err == nil {
			t.Errorf("Offer(%q) must fail", id)
		}
	}
}
