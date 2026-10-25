package lodging

import (
	"testing"
	"time"

	lodgingv1 "horkilion/gen/lodging/v1"
)

var nov7 = time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)

func TestOfferMatchesSearchPrice(t *testing.T) {
	stay := Stay{CheckIn: nov7, Nights: 3, Adults: 2, ChildAges: []int32{5}}
	hotels := SearchHotels("Сочи", stay, nil)
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
		if len(h.Sources) == 0 || h.Place == nil {
			t.Errorf("%s has no sources or place", h.Id)
		}
	}
}

func TestChildAgeChangesPrice(t *testing.T) {
	base := Stay{CheckIn: nov7, Nights: 1, Adults: 2}
	price := func(ages ...int32) int64 {
		s := base
		s.ChildAges = ages
		return SearchHotels("Сочи", s, nil)[0].TotalPriceRub
	}
	if price(1) != price() {
		t.Error("an infant must stay free")
	}
	if !(price() < price(6) && price(6) < price(14)) {
		t.Errorf("prices must grow with age: none=%d six=%d fourteen=%d", price(), price(6), price(14))
	}
}

func TestFilters(t *testing.T) {
	stay := Stay{CheckIn: nov7, Nights: 1, Adults: 1, Pets: true}
	for _, h := range SearchHotels("Казань", stay, &lodgingv1.Filters{FreeCancellationOnly: true, MinStars: 3}) {
		if !h.PetsAllowed || !h.FreeCancellation || h.Stars < 3 {
			t.Errorf("%s does not pass the filters but was returned", h.Name)
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
