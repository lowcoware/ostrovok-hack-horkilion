package transport

import (
	"context"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	commonv1 "horkilion/gen/common/v1"
	transportv1 "horkilion/gen/transport/v1"
	"horkilion/internal/dataset"
)

var bg = context.Background()

func provider(t *testing.T) *Synthetic {
	t.Helper()
	d, err := dataset.Load("../dataset/testdata/snapshot")
	if err != nil {
		t.Fatal(err)
	}
	return NewSynthetic(d)
}

func family() *commonv1.Travelers { return &commonv1.Travelers{Adults: 2, ChildAges: []int32{6}} }

func search(t *testing.T, s *Synthetic, from, to string, modes ...transportv1.Mode) []*transportv1.Journey {
	t.Helper()
	resp, err := s.SearchJourneys(bg, &transportv1.SearchJourneysRequest{Origin: from, Destination: to, Date: "2026-11-20", Passengers: family(), Modes: modes})
	if err != nil {
		t.Fatal(err)
	}
	return resp.GetJourneys()
}

func TestMoscowSochiHasFlightsAndTrainsThatMatchOffers(t *testing.T) {
	s := provider(t)
	got := search(t, s, "Москва", "Сочи")
	var air, rail int
	for _, j := range got {
		switch j.GetSegments()[0].GetMode() {
		case transportv1.Mode_MODE_AIR:
			air++
		case transportv1.Mode_MODE_RAIL:
			rail++
		}
		offer, err := s.GetOffer(bg, &transportv1.GetOfferRequest{JourneyId: j.GetId(), Passengers: family()})
		if err != nil || offer.GetPriceRub() != j.GetPriceRub() || offer.GetSegments()[0].GetDepartAt() != j.GetSegments()[0].GetDepartAt() {
			t.Errorf("%s: offer differs from search (%v)", j.GetId(), err)
		}
		if j.GetPriceRub() <= 0 || j.GetDurationMinutes() <= 0 || len(j.GetSources()) < 2 {
			t.Errorf("%s misses price, duration or sources", j.GetId())
		}
	}
	if air == 0 || rail != 2 {
		t.Errorf("want flights and the two trains of the timetable, got air=%d rail=%d", air, rail)
	}
}

func TestTimesAreInLocalZonesAndConsistent(t *testing.T) {
	s := provider(t)
	for _, j := range search(t, s, "Москва", "Пекин", transportv1.Mode_MODE_AIR) {
		segs := j.GetSegments()
		if len(segs) != 2 || j.GetTransfers() != 1 {
			t.Fatalf("Moscow to Beijing has no direct flight and must connect once: %v", j)
		}
		first, _ := time.Parse(time.RFC3339, segs[0].GetDepartAt())
		last, _ := time.Parse(time.RFC3339, segs[1].GetArriveAt())
		if int32(last.Sub(first).Minutes()) != j.GetDurationMinutes() {
			t.Errorf("%s: duration %d does not match the segments", j.GetId(), j.GetDurationMinutes())
		}
		if !strings.HasSuffix(segs[1].GetArriveAt(), "+08:00") || !strings.HasSuffix(segs[0].GetDepartAt(), "+03:00") {
			t.Errorf("%s: times must be local: %s .. %s", j.GetId(), segs[0].GetDepartAt(), segs[1].GetArriveAt())
		}
		mid, _ := time.Parse(time.RFC3339, segs[1].GetDepartAt())
		arr, _ := time.Parse(time.RFC3339, segs[0].GetArriveAt())
		if mid.Before(arr) {
			t.Errorf("%s: the second flight leaves before the first lands", j.GetId())
		}
	}
}

func TestReverseDirectionAndCodes(t *testing.T) {
	s := provider(t)
	if len(search(t, s, "Сочи", "Москва", transportv1.Mode_MODE_RAIL)) != 2 {
		t.Error("trains must run back from Сочи to Москва")
	}
	if len(search(t, s, "SVO", "AER", transportv1.Mode_MODE_AIR)) == 0 {
		t.Error("IATA codes must work as origin and destination")
	}
	if len(search(t, s, "Москва", "Атлантида")) != 0 {
		t.Error("an unknown city is an empty answer")
	}
}

func TestBadInput(t *testing.T) {
	s := provider(t)
	for name, req := range map[string]*transportv1.SearchJourneysRequest{
		"bad date":    {Origin: "Москва", Destination: "Сочи", Date: "20.11.2026", Passengers: family()},
		"no adults":   {Origin: "Москва", Destination: "Сочи", Date: "2026-11-20", Passengers: &commonv1.Travelers{}},
		"same city":   {Origin: "Москва", Destination: "SVO", Date: "2026-11-20", Passengers: family()},
		"child is 18": {Origin: "Москва", Destination: "Сочи", Date: "2026-11-20", Passengers: &commonv1.Travelers{Adults: 1, ChildAges: []int32{18}}},
	} {
		if _, err := s.SearchJourneys(bg, req); status.Code(err) != codes.InvalidArgument {
			t.Errorf("%s: want INVALID_ARGUMENT, got %v", name, err)
		}
	}
	for _, id := range []string{"", "air:SVO:AER:2026-11-20:9", "bus:a:b:2026-11-20:0", "air:SVO:AER:tomorrow:0"} {
		if _, err := s.GetOffer(bg, &transportv1.GetOfferRequest{JourneyId: id, Passengers: family()}); status.Code(err) != codes.NotFound {
			t.Errorf("%q: want NOT_FOUND, got %v", id, err)
		}
	}
}
