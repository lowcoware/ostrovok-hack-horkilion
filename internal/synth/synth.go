// Package synth holds the synthetic pricing and timing rules every data-backed
// supplier provider shares (spec 0003, CFC-5): real objects from the data
// snapshot, prices and durations generated on top of them from the
// calibration parameters. The same object, date and party always give the
// same result, so a search and a later GetOffer agree on the price.
package synth

import (
	"errors"
	"hash/fnv"
	"math"
	"math/rand/v2"
	"strconv"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "horkilion/gen/common/v1"
	"horkilion/internal/dataset"
)

// Party is who travels, in the terms the prices depend on.
type Party struct {
	Adults    int32
	ChildAges []int32
	Pets      bool
}

func PartyFromProto(t *commonv1.Travelers) Party {
	return Party{Adults: t.GetAdults(), ChildAges: t.GetChildAges(), Pets: t.GetPets()}
}

// ParseParty validates travellers coming over gRPC.
func ParseParty(t *commonv1.Travelers) (Party, error) {
	switch {
	case t.GetAdults() < 1 || t.GetAdults() > 10:
		return Party{}, errors.New("adults must be between 1 and 10")
	case len(t.GetChildAges()) > 10:
		return Party{}, errors.New("child_ages must have at most 10 items")
	}
	for _, age := range t.GetChildAges() {
		if age < 0 || age > 17 {
			return Party{}, errors.New("child_ages items must be between 0 and 17")
		}
	}
	return PartyFromProto(t), nil
}

// People counts everyone who takes a seat or a bed.
func (p Party) People() int {
	return int(p.Adults) + len(p.ChildAges)
}

// GuestWeight is the hotel price weight of the party: infants under 2 stay
// free, children under 12 pay half.
func (p Party) GuestWeight() float64 {
	w := float64(p.Adults)
	for _, age := range p.ChildAges {
		switch {
		case age < 2:
		case age < 12:
			w += 0.5
		default:
			w++
		}
	}
	return w
}

// FareWeight is the ticket price weight of the party: infants under 2 pay 10%,
// children under 12 pay 75%.
func (p Party) FareWeight() float64 {
	w := float64(p.Adults)
	for _, age := range p.ChildAges {
		switch {
		case age < 2:
			w += 0.1
		case age < 12:
			w += 0.75
		default:
			w++
		}
	}
	return w
}

// Rand is a generator seeded from the parts, stable across runs.
func Rand(parts ...string) *rand.Rand {
	h := fnv.New64a()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	s := h.Sum64()
	return rand.New(rand.NewPCG(s, s>>1|1))
}

// Noise is a stable multiplier in [lo, hi) for an object on a date.
func Noise(lo, hi float64, parts ...string) float64 {
	return lo + Rand(parts...).Float64()*(hi-lo)
}

// Season is the calibration multiplier of the month of d.
func Season(cal dataset.Calibration, d time.Time) float64 {
	if len(cal.SeasonFactor) != 12 {
		return 1
	}
	return cal.SeasonFactor[d.Month()-1]
}

// RoundRub rounds a price to whole hundreds of roubles.
func RoundRub(x float64) int64 {
	return int64(math.Round(x/100) * 100)
}

// Haversine is the great-circle distance in kilometres.
func Haversine(lat1, lon1, lat2, lon2 float64) float64 {
	const r = 6371.0
	rad := func(d float64) float64 { return d * math.Pi / 180 }
	dLat, dLon := rad(lat2-lat1), rad(lon2-lon1)
	h := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(rad(lat1))*math.Cos(rad(lat2))*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * r * math.Asin(math.Sqrt(h))
}

// FlightMinutes is the block time of a flight: cruise at 800 km/h plus 30
// minutes for taxi, climb and descent.
func FlightMinutes(distanceKm float64) int {
	return int(math.Round(distanceKm/800*60)) + 30
}

// Air is the timing of a journey by air on a route. A route without a direct
// flight goes through a hub: 15% longer and one change of calibration length.
func Air(cal dataset.Calibration, r dataset.Route) (minutes int, transfers int32) {
	if r.Direct {
		return FlightMinutes(r.DistanceKm), 0
	}
	return FlightMinutes(r.DistanceKm*1.15) + 30 + cal.Airfare.TransferMinutes, 1
}

// AirFareRub is the price of a journey by air for the party.
func AirFareRub(cal dataset.Calibration, r dataset.Route, p Party, date time.Time) int64 {
	a := cal.Airfare
	km := r.DistanceKm
	perKm := a.RubPerKmShort * min(km, a.LongFromKm)
	if km > a.LongFromKm {
		perKm += a.RubPerKmLong * (km - a.LongFromKm)
	}
	if !r.Direct {
		perKm *= 0.85 // connections are cheaper than a direct flight
	}
	noise := Noise(0.85, 1.25, "air", r.FromIATA, r.ToIATA, date.Format(time.DateOnly))
	return RoundRub((a.BaseRub + perKm) * Season(cal, date) * noise * p.FareWeight())
}

// RailFareRub is the price of a train journey for the party; children travel
// at half price, infants free.
func RailFareRub(cal dataset.Calibration, r dataset.RailRoute, train string, p Party, date time.Time) int64 {
	w := float64(p.Adults)
	for _, age := range p.ChildAges {
		if age >= 2 && age < 12 {
			w += 0.5
		} else if age >= 12 {
			w++
		}
	}
	noise := Noise(0.9, 1.2, "rail", r.FromStation, r.ToStation, train, date.Format(time.DateOnly))
	return RoundRub(cal.Rail.RubPerHour * float64(r.DurationMin) / 60 * Season(cal, date) * noise * w)
}

// BedRub is the price per guest per night of a hotel with the given stars in
// a country, before the hotel's own spread.
func BedRub(cal dataset.Calibration, stars int32, country string, date time.Time) float64 {
	return cal.BedPriceRub[strconv.Itoa(int(stars))] * cal.CountryPriceLevel[country] * Season(cal, date)
}

// TouristTaxRub is the tax for the stay in a city, zero where none applies.
func TouristTaxRub(cal dataset.Calibration, cityID string, p Party, nights int32) int64 {
	perNight := cal.TouristTaxRubPerNight[cityID]
	return int64(math.Round(perNight * float64(int(p.Adults)) * float64(nights)))
}

// Transfer timing and prices between a station or an airport and a hotel.
// City speed is 30 km/h on roads, 20 km/h for public transport plus waiting.
func TaxiMinutes(distanceKm float64) int    { return int(math.Round(distanceKm/30*60)) + 10 }
func ShuttleMinutes(distanceKm float64) int { return int(math.Round(distanceKm/30*60)) + 25 }
func PublicMinutes(distanceKm float64) int  { return int(math.Round(distanceKm/20*60)) + 20 }

func TaxiRub(cal dataset.Calibration, country string, distanceKm float64, p Party) int64 {
	t := cal.Transfer
	price := (t.TaxiBaseRub + t.TaxiRubPerKm*distanceKm) * cal.CountryPriceLevel[country]
	if p.People() > 4 {
		price *= 2 // a second car
	}
	if p.Pets {
		price += t.PetsSurchargeRub
	}
	return RoundRub(price)
}

func ShuttleRub(cal dataset.Calibration, country string, p Party) int64 {
	return RoundRub(cal.Transfer.ShuttleRubPerPerson * cal.CountryPriceLevel[country] * float64(p.People()))
}

func PublicRub(cal dataset.Calibration, country string, p Party) int64 {
	return RoundRub(cal.Transfer.PublicRubPerPerson * cal.CountryPriceLevel[country] * float64(p.People()))
}

// Sources expands provider names from the snapshot into API sources using the
// registry; scope says which fields they cover. Unknown names are skipped:
// dataset.Load already refuses a snapshot that references them.
func Sources(d *dataset.Data, scope string, names ...string) []*commonv1.Source {
	kinds := map[string]commonv1.Source_Kind{
		"real": commonv1.Source_KIND_REAL, "synthetic": commonv1.Source_KIND_SYNTHETIC, "derived": commonv1.Source_KIND_DERIVED,
	}
	out := make([]*commonv1.Source, 0, len(names))
	for _, n := range names {
		s := d.SourceByName[n]
		if s == nil {
			continue
		}
		src := &commonv1.Source{Provider: s.Provider, Kind: kinds[s.Kind], FieldScope: scope, Url: s.URL, License: s.License}
		if !s.FetchedAt.IsZero() {
			src.FetchedAt = timestamppb.New(s.FetchedAt)
		}
		out = append(out, src)
	}
	return out
}
