package synth

import (
	"testing"
	"time"

	"horkilion/internal/dataset"
)

func cal() dataset.Calibration {
	var c dataset.Calibration
	c.BedPriceRub = map[string]float64{"2": 1900, "3": 2700, "4": 4200, "5": 7500}
	c.CountryPriceLevel = map[string]float64{"RU": 1, "AE": 1.8}
	c.SeasonFactor = []float64{1, 1, 1, 1, 1, 1.3, 1.3, 1.3, 1, 1, 1, 1}
	c.Airfare.BaseRub, c.Airfare.RubPerKmShort, c.Airfare.RubPerKmLong, c.Airfare.LongFromKm = 2500, 7, 5, 2000
	c.Airfare.TransferMinutes = 120
	c.Rail.RubPerHour = 450
	c.Transfer.TaxiBaseRub, c.Transfer.TaxiRubPerKm, c.Transfer.PetsSurchargeRub = 300, 40, 500
	c.TouristTaxRubPerNight = map[string]float64{"sochi": 100}
	return c
}

var nov7 = time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)

func TestPricesAreStable(t *testing.T) {
	r := dataset.Route{FromIATA: "SVO", ToIATA: "AER", DistanceKm: 1360, Direct: true}
	p := Party{Adults: 2}
	if AirFareRub(cal(), r, p, nov7) != AirFareRub(cal(), r, p, nov7) {
		t.Fatal("same route, date and party must give the same fare")
	}
}

func TestFareGrowsWithChildAge(t *testing.T) {
	r := dataset.Route{FromIATA: "SVO", ToIATA: "AER", DistanceKm: 1360, Direct: true}
	price := func(ages ...int32) int64 { return AirFareRub(cal(), r, Party{Adults: 1, ChildAges: ages}, nov7) }
	if !(price() < price(1) && price(1) < price(6) && price(6) < price(14)) {
		t.Errorf("fares must grow with age: none=%d infant=%d six=%d fourteen=%d", price(), price(1), price(6), price(14))
	}
}

func TestIndirectFlightIsLongerAndHasAChange(t *testing.T) {
	direct, _ := Air(cal(), dataset.Route{DistanceKm: 3000, Direct: true})
	via, changes := Air(cal(), dataset.Route{DistanceKm: 3000, Direct: false})
	if via <= direct || changes != 1 {
		t.Errorf("a connection must be longer and have one change: direct=%d via=%d changes=%d", direct, via, changes)
	}
}

func TestSeasonAndCountryRaiseBedPrice(t *testing.T) {
	jul := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	if BedRub(cal(), 3, "RU", jul) <= BedRub(cal(), 3, "RU", nov7) {
		t.Error("summer must cost more than November")
	}
	if BedRub(cal(), 3, "AE", nov7) <= BedRub(cal(), 3, "RU", nov7) {
		t.Error("a pricier country must cost more")
	}
}

func TestTaxiWithPetsCostsMore(t *testing.T) {
	plain := TaxiRub(cal(), "RU", 20, Party{Adults: 2})
	pets := TaxiRub(cal(), "RU", 20, Party{Adults: 2, Pets: true})
	if pets <= plain {
		t.Errorf("pets must add a surcharge: %d vs %d", pets, plain)
	}
}

func TestTouristTaxOnlyWhereItApplies(t *testing.T) {
	if TouristTaxRub(cal(), "sochi", Party{Adults: 2}, 3) != 600 {
		t.Error("2 adults × 3 nights × 100 must be 600")
	}
	if TouristTaxRub(cal(), "paris", Party{Adults: 2}, 3) != 0 {
		t.Error("no tax where the city has none")
	}
}
