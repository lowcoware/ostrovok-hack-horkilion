// Package inventory implements InventoryService: synthetic hotels and real weather.
package inventory

import (
	"encoding/base64"
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	inventoryv1 "horkilion/gen/inventory/v1"
)

// impulse: hotels are generated from a seed instead of OSM points, switch to OSM-backed generation from docs/adr when the demo needs real hotel names.
const hotelsPerCity = 8

var (
	hotelTypes  = []string{"Отель", "Апарт-отель", "Гостевой дом", "Бутик-отель"}
	hotelNames  = []string{"Гранд", "Морской бриз", "Панорама", "Старый город", "Ривьера", "Альпина", "Лазурный", "Империал", "Уют", "Парк"}
	meals       = []string{"без питания", "завтрак", "завтрак и ужин", "всё включено"}
	pricePerBed = map[int32]float64{2: 1900, 3: 2700, 4: 4200, 5: 7500} // rub per person per night, calibrated on Rosstat averages
)

// Stay is a validated stay request.
type Stay struct {
	CheckIn  time.Time
	Nights   int32
	Adults   int32
	Children int32
	WithPets bool
}

// ParseStay validates a stay coming over gRPC.
func ParseStay(s *inventoryv1.Stay) (Stay, error) {
	if s == nil {
		return Stay{}, errors.New("stay is required")
	}
	checkIn, err := time.Parse(time.DateOnly, s.GetCheckIn())
	if err != nil {
		return Stay{}, fmt.Errorf("check_in must be YYYY-MM-DD: %w", err)
	}
	switch {
	case s.GetNights() < 1 || s.GetNights() > 30:
		return Stay{}, errors.New("nights must be between 1 and 30")
	case s.GetAdults() < 1 || s.GetAdults() > 10:
		return Stay{}, errors.New("adults must be between 1 and 10")
	case s.GetChildren() < 0 || s.GetChildren() > 10:
		return Stay{}, errors.New("children must be between 0 and 10")
	}
	return Stay{CheckIn: checkIn, Nights: s.GetNights(), Adults: s.GetAdults(), Children: s.GetChildren(), WithPets: s.GetWithPets()}, nil
}

// NormalizeCity trims the name and validates its length.
func NormalizeCity(city string) (string, error) {
	city = strings.TrimSpace(city)
	if city == "" || len([]rune(city)) > 100 {
		return "", errors.New("city must be 1 to 100 characters")
	}
	return city, nil
}

// SearchHotels returns the hotels of a city priced for the stay, cheapest first.
// Hotels that do not accept pets are dropped when the stay includes pets.
func SearchHotels(city string, stay Stay) []*inventoryv1.Hotel {
	hotels := make([]*inventoryv1.Hotel, 0, hotelsPerCity)
	for i := range hotelsPerCity {
		h := hotel(city, i, stay)
		if stay.WithPets && !h.PetsAllowed {
			continue
		}
		hotels = append(hotels, h)
	}
	slices.SortFunc(hotels, func(a, b *inventoryv1.Hotel) int { return int(a.TotalPriceRub - b.TotalPriceRub) })
	return hotels
}

// Offer re-prices one hotel by its ID.
func Offer(hotelID string, stay Stay) (*inventoryv1.Hotel, error) {
	city, idx, err := parseHotelID(hotelID)
	if err != nil {
		return nil, err
	}
	h := hotel(city, idx, stay)
	if stay.WithPets && !h.PetsAllowed {
		return nil, errors.New("hotel does not accept pets")
	}
	return h, nil
}

func hotel(city string, idx int, stay Stay) *inventoryv1.Hotel {
	r := seeded(strings.ToLower(city), strconv.Itoa(idx))
	stars := int32(2 + r.IntN(4))
	cityFactor := 0.8 + seeded(strings.ToLower(city)).Float64()*0.7
	hotelFactor := 0.85 + r.Float64()*0.35
	guests := float64(stay.Adults) + 0.5*float64(stay.Children)

	perNight := pricePerBed[stars] * guests * cityFactor * hotelFactor * seasonFactor(stay.CheckIn)
	petsAllowed := r.IntN(3) == 0
	if stay.WithPets && petsAllowed {
		perNight += 1000
	}
	perNightRub := int64(math.Round(perNight/100) * 100)

	return &inventoryv1.Hotel{
		Id:                 hotelID(city, idx),
		Name:               fmt.Sprintf("%s «%s»", hotelTypes[r.IntN(len(hotelTypes))], hotelNames[(idx+r.IntN(3))%len(hotelNames)]),
		City:               city,
		Stars:              stars,
		Rating:             math.Round((6.5+r.Float64()*3.4)*10) / 10,
		Meal:               meals[r.IntN(len(meals))],
		FreeCancellation:   r.IntN(2) == 0,
		PetsAllowed:        petsAllowed,
		PricePerNightRub:   perNightRub,
		TotalPriceRub:      perNightRub * int64(stay.Nights),
		DistanceToCenterKm: math.Round(r.Float64()*80) / 10,
		Synthetic:          true,
	}
}

func seasonFactor(d time.Time) float64 {
	switch m := d.Month(); {
	case m >= time.June && m <= time.August:
		return 1.35
	case (m == time.December && d.Day() >= 20) || (m == time.January && d.Day() <= 10):
		return 1.4
	case m == time.May || m == time.September:
		return 1.15
	default:
		return 1.0
	}
}

func seeded(parts ...string) *rand.Rand {
	h := fnv.New64a()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	s := h.Sum64()
	return rand.New(rand.NewPCG(s, s>>1|1))
}

func hotelID(city string, idx int) string {
	return "syn." + base64.RawURLEncoding.EncodeToString([]byte(city)) + "." + strconv.Itoa(idx)
}

func parseHotelID(id string) (string, int, error) {
	parts := strings.Split(id, ".")
	if len(parts) != 3 || parts[0] != "syn" {
		return "", 0, errors.New("unknown hotel id")
	}
	city, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !utf8.Valid(city) {
		return "", 0, errors.New("unknown hotel id")
	}
	if _, err := NormalizeCity(string(city)); err != nil {
		return "", 0, errors.New("unknown hotel id")
	}
	idx, err := strconv.Atoi(parts[2])
	if err != nil || idx < 0 || idx >= hotelsPerCity {
		return "", 0, errors.New("unknown hotel id")
	}
	return string(city), idx, nil
}
