package lodging

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	commonv1 "horkilion/gen/common/v1"
	lodgingv1 "horkilion/gen/lodging/v1"
	"horkilion/internal/dataset"
	"horkilion/internal/synth"
)

// OSM is the provider "osm": real hotels from OpenStreetMap in the data
// snapshot; stars, meals, cancellation, pets and prices that OSM lacks are
// generated per hotel and date by the synth rules.
type OSM struct {
	lodgingv1.UnimplementedLodgingServiceServer
	data *dataset.Data
}

func NewOSM(data *dataset.Data) *OSM { return &OSM{data: data} }

// petsSurchargeRub is the extra per night for a pet, at the Russian price level.
const petsSurchargeRub = 1000

func (s *OSM) SearchHotels(_ context.Context, req *lodgingv1.SearchHotelsRequest) (*lodgingv1.SearchHotelsResponse, error) {
	city, err := NormalizeCity(req.GetCity())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	stay, err := ParseStay(req.GetStay())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	dest := s.destination(city)
	if dest == nil {
		// Not in the catalog: an empty answer, the agent tells the user.
		return &lodgingv1.SearchHotelsResponse{}, nil
	}
	f := req.GetFilters()
	var hotels []*lodgingv1.Hotel
	for _, h := range s.data.HotelsByDest[dest.ID] {
		offer := s.offer(h, stay)
		switch {
		case stay.Pets && !offer.PetsAllowed,
			f.GetFreeCancellationOnly() && !offer.FreeCancellation,
			f.GetMeal() != "" && offer.Meal != f.GetMeal(),
			offer.Stars < f.GetMinStars(),
			f.GetMaxTotalRub() > 0 && offer.TotalPriceRub > f.GetMaxTotalRub():
			continue
		}
		hotels = append(hotels, offer)
	}
	slices.SortFunc(hotels, func(a, b *lodgingv1.Hotel) int { return cmp.Compare(a.TotalPriceRub, b.TotalPriceRub) })
	return &lodgingv1.SearchHotelsResponse{Hotels: hotels}, nil
}

func (s *OSM) GetOffer(_ context.Context, req *lodgingv1.GetOfferRequest) (*lodgingv1.Hotel, error) {
	stay, err := ParseStay(req.GetStay())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	h := s.data.HotelByID[req.GetHotelId()]
	if h == nil {
		return nil, status.Error(codes.NotFound, "unknown hotel id")
	}
	offer := s.offer(h, stay)
	if stay.Pets && !offer.PetsAllowed {
		return nil, status.Error(codes.NotFound, "hotel does not accept pets")
	}
	return offer, nil
}

// destination finds a catalog destination by id, its name or its city name.
func (s *OSM) destination(name string) *dataset.Destination {
	q := strings.ReplaceAll(strings.ToLower(name), "ё", "е")
	match := func(v string) bool { return strings.ReplaceAll(strings.ToLower(v), "ё", "е") == q }
	for i := range s.data.Destinations {
		d := &s.data.Destinations[i]
		c := s.data.CityByID[d.CityID]
		if d.ID == q || match(d.Name) || match(c.NameRu) || match(c.NameEn) {
			return d
		}
	}
	return nil
}

var mealCodes = []string{"room_only", "breakfast", "half_board", "all_inclusive"}

// offer prices one hotel for the stay. Everything random is seeded by the
// hotel id, and the price also by the check-in date, so search and GetOffer
// always agree.
func (s *OSM) offer(h *dataset.Hotel, stay Stay) *lodgingv1.Hotel {
	cal := s.data.Calibration
	dest := s.data.DestinationByID[h.DestinationID]
	city := s.data.CityByID[dest.CityID]
	r := synth.Rand("hotel", h.ID)

	stars := int32(2 + r.IntN(4))
	if h.Stars != nil && *h.Stars >= 2 && *h.Stars <= 5 {
		stars = *h.Stars
	}
	pets := r.IntN(3) == 0
	if h.Pets != nil {
		pets = *h.Pets
	}
	meal := mealCodes[r.IntN(len(mealCodes))]
	freeCancel := r.IntN(2) == 0
	rating := math.Round((6.5+r.Float64()*3.4)*10) / 10
	checkIn, checkOut := cmp.Or(h.CheckInTime, "14:00"), cmp.Or(h.CheckOutTime, "12:00")

	party := synth.Party{Adults: stay.Adults, ChildAges: stay.ChildAges, Pets: stay.Pets}
	level := cal.CountryPriceLevel[dest.CountryCode]
	perNight := synth.BedRub(cal, stars, dest.CountryCode, stay.CheckIn) * party.GuestWeight() *
		synth.Noise(0.85, 1.2, "hotel-price", h.ID, stay.CheckIn.Format(time.DateOnly))
	if stay.Pets && pets {
		perNight += petsSurchargeRub * level
	}
	perNightRub := synth.RoundRub(perNight)
	tax := synth.TouristTaxRub(cal, dest.CityID, party, stay.Nights)

	policy := "невозвратный тариф: при отмене стоимость не возвращается"
	if freeCancel {
		policy = "бесплатная отмена до 18:00 за сутки до заезда"
	}
	if tax > 0 {
		policy += fmt.Sprintf("; в цену включён туристический налог %d ₽", tax)
	}
	return &lodgingv1.Hotel{
		Id:   h.ID,
		Name: h.Name,
		City: city.NameRu,
		Place: &commonv1.Place{
			Id: h.ID, Name: h.Name, Kind: commonv1.Place_KIND_HOTEL, CountryCode: dest.CountryCode,
			Point: &commonv1.GeoPoint{Lat: h.Lat, Lon: h.Lon}, Timezone: city.Timezone,
		},
		Stars:              stars,
		Rating:             rating,
		Meal:               meal,
		FreeCancellation:   freeCancel,
		CancellationPolicy: policy,
		PetsAllowed:        pets,
		CheckInTime:        checkIn,
		CheckOutTime:       checkOut,
		PricePerNightRub:   perNightRub,
		TotalPriceRub:      perNightRub*int64(stay.Nights) + tax,
		DistanceToCenterKm: math.Round(synth.Haversine(city.Lat, city.Lon, h.Lat, h.Lon)*10) / 10,
		Sources: append(synth.Sources(s.data, "location,name", h.Sources...),
			synth.Sources(s.data, "price,stars,meal,cancellation", "synthetic")...),
	}
}
