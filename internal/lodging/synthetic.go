// Package lodging implements lodgingv1.LodgingService providers.
package lodging

import (
	"context"
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

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	commonv1 "horkilion/gen/common/v1"
	lodgingv1 "horkilion/gen/lodging/v1"
)

// impulse: hotels are generated from a seed instead of OSM points, switch to OSM-backed generation from docs/adr when the demo needs real hotel names.
const hotelsPerCity = 8

var (
	hotelTypes      = []string{"Отель", "Апарт-отель", "Гостевой дом", "Бутик-отель"}
	hotelNames      = []string{"Гранд", "Морской бриз", "Панорама", "Старый город", "Ривьера", "Альпина", "Лазурный", "Империал", "Уют", "Парк"}
	meals           = []string{"room_only", "breakfast", "half_board", "all_inclusive"}
	pricePerBed     = map[int32]float64{2: 1900, 3: 2700, 4: 4200, 5: 7500} // rub per person per night, calibrated on Rosstat averages
	syntheticSource = &commonv1.Source{Provider: "synthetic", Kind: commonv1.Source_KIND_SYNTHETIC, FieldScope: "*"}
)

// Synthetic is the provider "synthetic": deterministic generated hotels. The
// same city, hotel and stay always give the same price, so GetOffer matches
// SearchHotels.
type Synthetic struct {
	lodgingv1.UnimplementedLodgingServiceServer
}

// Stay is a validated stay request.
type Stay struct {
	CheckIn   time.Time
	Nights    int32
	Adults    int32
	ChildAges []int32
	Pets      bool
}

// ParseStay validates a stay coming over gRPC.
func ParseStay(s *lodgingv1.Stay) (Stay, error) {
	if s == nil {
		return Stay{}, errors.New("stay is required")
	}
	checkIn, err := time.Parse(time.DateOnly, s.GetCheckIn())
	if err != nil {
		return Stay{}, fmt.Errorf("check_in must be YYYY-MM-DD: %w", err)
	}
	t := s.GetTravelers()
	switch {
	case s.GetNights() < 1 || s.GetNights() > 30:
		return Stay{}, errors.New("nights must be between 1 and 30")
	case t.GetAdults() < 1 || t.GetAdults() > 10:
		return Stay{}, errors.New("travelers.adults must be between 1 and 10")
	case len(t.GetChildAges()) > 10:
		return Stay{}, errors.New("travelers.child_ages must have at most 10 items")
	}
	for _, age := range t.GetChildAges() {
		if age < 0 || age > 17 {
			return Stay{}, errors.New("travelers.child_ages items must be between 0 and 17")
		}
	}
	return Stay{CheckIn: checkIn, Nights: s.GetNights(), Adults: t.GetAdults(), ChildAges: t.GetChildAges(), Pets: t.GetPets()}, nil
}

// NormalizeCity trims the name and validates its length.
func NormalizeCity(city string) (string, error) {
	city = strings.TrimSpace(city)
	if city == "" || utf8.RuneCountInString(city) > 100 {
		return "", errors.New("city must be 1 to 100 characters")
	}
	return city, nil
}

func (Synthetic) SearchHotels(_ context.Context, req *lodgingv1.SearchHotelsRequest) (*lodgingv1.SearchHotelsResponse, error) {
	city, err := NormalizeCity(req.GetCity())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	stay, err := ParseStay(req.GetStay())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	return &lodgingv1.SearchHotelsResponse{Hotels: SearchHotels(city, stay, req.GetFilters())}, nil
}

func (Synthetic) GetOffer(_ context.Context, req *lodgingv1.GetOfferRequest) (*lodgingv1.Hotel, error) {
	stay, err := ParseStay(req.GetStay())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	h, err := Offer(req.GetHotelId(), stay)
	if err != nil {
		return nil, status.Error(codes.NotFound, err.Error())
	}
	return h, nil
}

// SearchHotels returns the hotels of a city priced for the stay, cheapest first.
// Hotels that do not accept pets are dropped when the stay includes pets.
func SearchHotels(city string, stay Stay, f *lodgingv1.Filters) []*lodgingv1.Hotel {
	hotels := make([]*lodgingv1.Hotel, 0, hotelsPerCity)
	for i := range hotelsPerCity {
		h := hotel(city, i, stay)
		switch {
		case stay.Pets && !h.PetsAllowed,
			f.GetFreeCancellationOnly() && !h.FreeCancellation,
			f.GetMeal() != "" && h.Meal != f.GetMeal(),
			h.Stars < f.GetMinStars(),
			f.GetMaxTotalRub() > 0 && h.TotalPriceRub > f.GetMaxTotalRub():
			continue
		}
		hotels = append(hotels, h)
	}
	slices.SortFunc(hotels, func(a, b *lodgingv1.Hotel) int { return int(a.TotalPriceRub - b.TotalPriceRub) })
	return hotels
}

// Offer re-prices one hotel by its ID.
func Offer(hotelID string, stay Stay) (*lodgingv1.Hotel, error) {
	city, idx, err := parseHotelID(hotelID)
	if err != nil {
		return nil, err
	}
	h := hotel(city, idx, stay)
	if stay.Pets && !h.PetsAllowed {
		return nil, errors.New("hotel does not accept pets")
	}
	return h, nil
}

// guests counts children by age: infants stay free, children under 12 pay half.
func guests(stay Stay) float64 {
	n := float64(stay.Adults)
	for _, age := range stay.ChildAges {
		switch {
		case age < 2:
		case age < 12:
			n += 0.5
		default:
			n++
		}
	}
	return n
}

func hotel(city string, idx int, stay Stay) *lodgingv1.Hotel {
	r := seeded(strings.ToLower(city), strconv.Itoa(idx))
	stars := int32(2 + r.IntN(4))
	cityFactor := 0.8 + seeded(strings.ToLower(city)).Float64()*0.7
	hotelFactor := 0.85 + r.Float64()*0.35

	perNight := pricePerBed[stars] * guests(stay) * cityFactor * hotelFactor * seasonFactor(stay.CheckIn)
	petsAllowed := r.IntN(3) == 0
	if stay.Pets && petsAllowed {
		perNight += 1000
	}
	perNightRub := int64(math.Round(perNight/100) * 100)

	name := fmt.Sprintf("%s «%s»", hotelTypes[r.IntN(len(hotelTypes))], hotelNames[(idx+r.IntN(3))%len(hotelNames)])
	rating := math.Round((6.5+r.Float64()*3.4)*10) / 10
	meal := meals[r.IntN(len(meals))]
	freeCancel := r.IntN(2) == 0
	distance := math.Round(r.Float64()*80) / 10
	policy := "невозвратный тариф: при отмене стоимость не возвращается"
	if freeCancel {
		policy = "бесплатная отмена до 18:00 за сутки до заезда"
	}
	id := hotelID(city, idx)
	return &lodgingv1.Hotel{
		Id:                 id,
		Name:               name,
		City:               city,
		Place:              &commonv1.Place{Id: id, Name: name, Kind: commonv1.Place_KIND_HOTEL},
		Stars:              stars,
		Rating:             rating,
		Meal:               meal,
		FreeCancellation:   freeCancel,
		CancellationPolicy: policy,
		PetsAllowed:        petsAllowed,
		CheckInTime:        "14:00",
		CheckOutTime:       "12:00",
		PricePerNightRub:   perNightRub,
		TotalPriceRub:      perNightRub * int64(stay.Nights),
		DistanceToCenterKm: distance,
		Sources:            []*commonv1.Source{syntheticSource},
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
