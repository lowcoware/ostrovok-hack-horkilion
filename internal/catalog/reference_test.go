package catalog

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	catalogv1 "horkilion/gen/catalog/v1"
	commonv1 "horkilion/gen/common/v1"
	"horkilion/internal/dataset"
)

var ctx = context.Background()

func provider(t *testing.T) (*OpenData, *dataset.Data) {
	t.Helper()
	d, err := dataset.Load("../dataset/testdata/snapshot")
	if err != nil {
		t.Fatal(err)
	}
	return NewOpenData(d), d
}

func search(t *testing.T, s *OpenData, req *catalogv1.SearchDestinationsRequest) []*catalogv1.Destination {
	t.Helper()
	if req.Origin == "" {
		req.Origin = "Москва"
	}
	if req.DepartDate == "" {
		req.DepartDate = "2026-11-20"
	}
	req.Limit = 20
	resp, err := s.SearchDestinations(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	return resp.GetDestinations()
}

func TestWarmSeaInNovemberForRussianPassport(t *testing.T) {
	s, _ := provider(t)
	got := search(t, s, &catalogv1.SearchDestinationsRequest{Tags: []string{"sea", "warm"}, VisaFreeOnly: true,
		Travelers: &commonv1.Travelers{Adults: 2, ChildAges: []int32{6}}})
	if len(got) == 0 {
		t.Fatal("want warm sea destinations")
	}
	for _, d := range got {
		if !visaFree(d.GetVisa().GetStatus()) {
			t.Errorf("%s: visa_free_only must exclude %s", d.GetName(), d.GetVisa().GetStatus())
		}
		if !hasAll(d.GetTags(), []string{"sea", "warm"}) {
			t.Errorf("%s has no sea and warm tags", d.GetName())
		}
		if d.GetWeather() == nil || d.GetEstimatedTotalRub() <= 0 || d.GetTravelHours() <= 0 || len(d.GetVisa().GetSources()) == 0 {
			t.Errorf("%s misses weather, estimate, travel time or visa sources: %v", d.GetName(), d)
		}
	}
}

func TestVisaFilterAndCitizenship(t *testing.T) {
	s, _ := provider(t)
	names := func(ds []*catalogv1.Destination) map[string]catalogv1.VisaRequirement_Status {
		m := map[string]catalogv1.VisaRequirement_Status{}
		for _, d := range ds {
			m[d.GetId()] = d.GetVisa().GetStatus()
		}
		return m
	}
	free := names(search(t, s, &catalogv1.SearchDestinationsRequest{VisaFreeOnly: true}))
	all := names(search(t, s, &catalogv1.SearchDestinationsRequest{VisaFreeOnly: false}))
	if _, ok := free["paris"]; ok {
		t.Error("Paris needs a visa for a Russian passport and must be filtered out")
	}
	if all["paris"] != catalogv1.VisaRequirement_STATUS_VISA_REQUIRED {
		t.Errorf("without the filter Paris must be offered with visa_required, got %v", all["paris"])
	}

	by := search(t, s, &catalogv1.SearchDestinationsRequest{Citizenship: "BY", Origin: "Минск"})
	for _, d := range by {
		if d.GetVisa().GetCitizenship() != "BY" {
			t.Errorf("visa must be for the Belarusian passport, got %s", d.GetVisa().GetCitizenship())
		}
	}
	for _, d := range search(t, s, &catalogv1.SearchDestinationsRequest{Origin: "Минск"}) {
		if d.GetId() == "minsk" {
			t.Error("the origin city must not be suggested")
		}
	}
}

func TestNoAdmissionIsNeverOffered(t *testing.T) {
	s, d := provider(t)
	row, ok := d.Visa("RU", "TR")
	if !ok {
		t.Fatal("test data needs RU/TR")
	}
	row.Status = "no_admission"
	for _, x := range search(t, s, &catalogv1.SearchDestinationsRequest{VisaFreeOnly: false}) {
		if x.GetCountryCode() == "TR" {
			t.Error("a country with no admission must never be offered")
		}
	}
}

func TestBudgetAndTravelTimeFilters(t *testing.T) {
	s, _ := provider(t)
	all := search(t, s, &catalogv1.SearchDestinationsRequest{VisaFreeOnly: false})
	cheapest := all[0].GetEstimatedTotalRub()
	for _, d := range search(t, s, &catalogv1.SearchDestinationsRequest{VisaFreeOnly: false, BudgetRub: cheapest}) {
		if d.GetEstimatedTotalRub() > cheapest {
			t.Errorf("%s costs %d over the budget %d", d.GetName(), d.GetEstimatedTotalRub(), cheapest)
		}
	}
	for _, d := range search(t, s, &catalogv1.SearchDestinationsRequest{VisaFreeOnly: false, MaxTravelHours: 4}) {
		if d.GetTravelHours() > 4 {
			t.Errorf("%s takes %.1f hours", d.GetName(), d.GetTravelHours())
		}
	}
}

func TestSearchDestinationsRejectsBadInput(t *testing.T) {
	s, _ := provider(t)
	for name, req := range map[string]*catalogv1.SearchDestinationsRequest{
		"unknown origin":      {Origin: "Атлантида", DepartDate: "2026-11-20"},
		"unknown citizenship": {Origin: "Москва", DepartDate: "2026-11-20", Citizenship: "XX"},
		"no dates":            {Origin: "Москва"},
	} {
		if _, err := s.SearchDestinations(ctx, req); status.Code(err) != codes.InvalidArgument {
			t.Errorf("%s: want INVALID_ARGUMENT, got %v", name, err)
		}
	}
}

func TestGetVisaRequirement(t *testing.T) {
	s, _ := provider(t)
	v, err := s.GetVisaRequirement(ctx, &catalogv1.GetVisaRequirementRequest{Citizenship: "RU", CountryCode: "AE"})
	if err != nil || v.GetStatus() != catalogv1.VisaRequirement_STATUS_VISA_FREE || v.GetMaxStayDays() <= 0 || len(v.GetSources()) == 0 {
		t.Fatalf("RU to AE must be visa free with days and sources: %v, %v", v, err)
	}
	if v, err := s.GetVisaRequirement(ctx, &catalogv1.GetVisaRequirementRequest{Citizenship: "FR", CountryCode: "FR"}); err != nil ||
		v.GetStatus() != catalogv1.VisaRequirement_STATUS_HOME {
		t.Errorf("own country must be home: %v, %v", v, err)
	}
	if _, err := s.GetVisaRequirement(ctx, &catalogv1.GetVisaRequirementRequest{Citizenship: "ru", CountryCode: "TR"}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("lower-case code must be INVALID_ARGUMENT, got %v", err)
	}
	if _, err := s.GetVisaRequirement(ctx, &catalogv1.GetVisaRequirementRequest{Citizenship: "FR", CountryCode: "TR"}); status.Code(err) != codes.NotFound {
		t.Errorf("a pair missing from the data must be NOT_FOUND, got %v", err)
	}
}

func TestListCountriesStartsWithRussia(t *testing.T) {
	s, d := provider(t)
	resp, _ := s.ListCountries(ctx, &catalogv1.ListCountriesRequest{})
	if len(resp.GetCountries()) != len(d.Countries) || resp.GetCountries()[0].GetCode() != "RU" {
		t.Errorf("want all countries with RU first, got %v", resp.GetCountries())
	}
}

func TestSearchPlaces(t *testing.T) {
	s, _ := provider(t)
	resp, err := s.SearchPlaces(ctx, &catalogv1.SearchPlacesRequest{Query: "мин"})
	if err != nil || len(resp.GetPlaces()) == 0 || resp.GetPlaces()[0].GetName() != "Минск" {
		t.Fatalf("«мин» must find Минск: %v, %v", resp, err)
	}
	if resp, _ := s.SearchPlaces(ctx, &catalogv1.SearchPlacesRequest{Query: "Mos"}); len(resp.GetPlaces()) == 0 {
		t.Error("an English prefix must find Moscow")
	}
	if _, err := s.SearchPlaces(ctx, &catalogv1.SearchPlacesRequest{Query: "М"}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("one letter must be INVALID_ARGUMENT, got %v", err)
	}
}
