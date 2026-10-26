package plan

import (
	"testing"

	"horkilion/internal/apitest"
)

func checkOf(p TripPlan, kind string) *Check {
	for i := range p.Checks {
		if p.Checks[i].Kind == kind {
			return &p.Checks[i]
		}
	}
	return nil
}

func TestCitizenshipDefaultsToRussiaAsAssumption(t *testing.T) {
	p := New("s")
	p.Apply(Patch{Destination: ptr("Сочи")}, today)
	p.FillDefaults(today)
	if p.Constraints.Citizenship != "RU" {
		t.Fatalf("want RU by default, got %q", p.Constraints.Citizenship)
	}
	found := false
	for _, a := range p.Assumptions {
		found = found || a.Field == "citizenship"
	}
	if !found {
		t.Errorf("default citizenship must be a visible assumption: %+v", p.Assumptions)
	}
	p.Apply(Patch{Citizenship: ptr("BY")}, today)
	for _, a := range p.Assumptions {
		if a.Field == "citizenship" {
			t.Errorf("an explicit citizenship must drop the assumption")
		}
	}
}

func TestCitizenshipChangeKeepsHotelsAndDropsVisa(t *testing.T) {
	p := searchedPlan(t)
	p.DestinationOptions = []DestinationOption{{Destination: Destination{ID: "sochi"}}}
	p.SetVisa(&VisaRequirement{Citizenship: "RU", CountryCode: "RU", Status: VisaHome, Sources: []Source{}})

	p.Apply(Patch{Citizenship: ptr("BY")}, today)
	if len(p.Components.Stay.Options) == 0 || p.NeedsSearch(Stay) {
		t.Error("citizenship must not touch hotels")
	}
	if p.Visa != nil || len(p.DestinationOptions) != 0 || checkOf(p, CheckVisa) != nil {
		t.Errorf("citizenship must drop visa, its checks and destination options: visa=%v options=%d", p.Visa, len(p.DestinationOptions))
	}
}

func TestVisaFreeOnlyDefaultsOnAndResetsOptions(t *testing.T) {
	p := New("s")
	if !p.Constraints.Preferences.VisaFreeOnly {
		t.Fatal("visa_free_only must default to true")
	}
	p.DestinationOptions = []DestinationOption{{Destination: Destination{ID: "x"}}}
	p.Apply(Patch{Preferences: &PreferencesPatch{VisaFreeOnly: ptr(false)}}, today)
	if p.Constraints.Preferences.VisaFreeOnly || len(p.DestinationOptions) != 0 {
		t.Errorf("turning the filter off must reset destination options")
	}
}

func TestVisaChecks(t *testing.T) {
	tests := []struct {
		status    string
		want      string
		stayCheck bool
	}{
		{VisaHome, "ok", false},
		{VisaFree, "ok", true},
		{VisaOnArrival, "ok", true},
		{VisaETA, "warn", false},
		{VisaElectronic, "warn", false},
		{VisaRequired, "fail", false},
		{VisaNoAdmission, "fail", false},
	}
	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			p := searchedPlan(t)
			p.SetVisa(&VisaRequirement{Citizenship: "RU", CountryCode: "TR", Status: tt.status, MaxStayDays: 60, Sources: []Source{}})
			c := checkOf(p, CheckVisa)
			if c == nil || c.Status != tt.want {
				t.Fatalf("visa check: want %s, got %+v", tt.want, c)
			}
			if got := checkOf(p, CheckVisaFreeStay) != nil; got != tt.stayCheck {
				t.Errorf("stay check present = %v, want %v", got, tt.stayCheck)
			}
		})
	}
}

func TestStayLongerThanVisaFreeDaysFails(t *testing.T) {
	p := searchedPlan(t)
	p.SetVisa(&VisaRequirement{Citizenship: "RU", CountryCode: "AE", Status: VisaFree, MaxStayDays: 5, Sources: []Source{}})
	if c := checkOf(p, CheckVisaFreeStay); c == nil || c.Status != "ok" {
		t.Fatalf("3 nights within 5 days must pass, got %+v", c)
	}
	p.Apply(Patch{Dates: &DatesPatch{Nights: ptr(int32(7))}}, today)
	if c := checkOf(p, CheckVisaFreeStay); c == nil || c.Status != "fail" {
		t.Errorf("7 nights over 5 days must fail, got %+v", c)
	}
}

func TestUncheckedVisaWarns(t *testing.T) {
	p := searchedPlan(t)
	p.SetVisa(nil)
	if c := checkOf(p, CheckVisa); c == nil || c.Status != "warn" {
		t.Errorf("a failed visa lookup must be a warning, got %+v", c)
	}
	p.Apply(Patch{Destination: ptr("Казань")}, today)
	if checkOf(p, CheckVisa) != nil {
		t.Error("a new destination must drop the old visa checks")
	}
}

func TestCitizenshipValidation(t *testing.T) {
	for _, bad := range []string{"ru", "RUS", "", "R1"} {
		if (Patch{Citizenship: ptr(bad)}).Validate() == nil {
			t.Errorf("citizenship %q must be rejected", bad)
		}
	}
	if err := (Patch{Citizenship: ptr("KZ")}).Validate(); err != nil {
		t.Errorf("KZ is valid: %v", err)
	}
}

func TestVisaPlanMatchesSchema(t *testing.T) {
	p := searchedPlan(t)
	p.Apply(Patch{Citizenship: ptr("BY")}, today)
	visa := VisaRequirement{Citizenship: "BY", CountryCode: "CN", Status: VisaFree, MaxStayDays: 30,
		Sources: []Source{{Provider: "passport-index-data", Kind: "real", FieldScope: "visa"}}}
	p.SetVisa(&visa)
	p.DestinationOptions = []DestinationOption{{Destination: Destination{
		ID: "beijing", Name: "Пекин", Country: "Китай", CountryCode: "CN", Place: Place{Name: "Пекин", Kind: "city"},
		Tags: []string{"city"}, EstimatedTotalRub: 150000, TravelHours: 7, Visa: visa, Sources: []Source{},
	}}}
	apitest.ValidateSchema(t, "TripPlan", p)
	apitest.ValidateSchema(t, "PlanPatch", Patch{Citizenship: ptr("BY"), Preferences: &PreferencesPatch{VisaFreeOnly: ptr(false)}})
}
