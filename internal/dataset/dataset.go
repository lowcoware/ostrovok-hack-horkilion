// Package dataset loads the read-only data snapshot the supplier providers
// serve from: directories of JSON Lines files with a manifest, in the format
// of spec 0003 (CFC-3). The data role builds the snapshot in data/; the
// providers only read it, at start, into memory.
package dataset

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/goccy/go-yaml"
)

type Manifest struct {
	BuiltAt  time.Time     `json:"built_at"`
	Datasets []DatasetInfo `json:"datasets"`
}

type DatasetInfo struct {
	Name          string `json:"name"`
	File          string `json:"file"`
	Records       int    `json:"records"`
	SHA256        string `json:"sha256"`
	SchemaVersion int    `json:"schema_version"`
}

type Country struct {
	Code   string `json:"code"`
	NameRu string `json:"name_ru"`
	NameEn string `json:"name_en"`
}

// Visa is one passport-destination pair. Status uses the API values of spec
// 0002: home, visa_free, visa_on_arrival, eta, e_visa, visa_required, no_admission.
type Visa struct {
	Passport    string   `json:"passport"`
	Destination string   `json:"destination"`
	Status      string   `json:"status"`
	Days        int32    `json:"days,omitempty"`
	Sources     []string `json:"sources"`
}

type City struct {
	ID          string   `json:"id"`
	NameRu      string   `json:"name_ru"`
	NameEn      string   `json:"name_en"`
	CountryCode string   `json:"country_code"`
	Lat         float64  `json:"lat"`
	Lon         float64  `json:"lon"`
	Timezone    string   `json:"timezone"`
	Population  int      `json:"population"`
	Airports    []string `json:"airports"`
	Stations    []string `json:"stations"`
}

type Airport struct {
	IATA        string  `json:"iata"`
	Name        string  `json:"name"`
	CityID      string  `json:"city_id"`
	CountryCode string  `json:"country_code"`
	Lat         float64 `json:"lat"`
	Lon         float64 `json:"lon"`
	Timezone    string  `json:"timezone"`
	Scheduled   bool    `json:"scheduled"`
}

type Station struct {
	Code   string  `json:"code"`
	Name   string  `json:"name"`
	CityID string  `json:"city_id"`
	Lat    float64 `json:"lat"`
	Lon    float64 `json:"lon"`
}

type Destination struct {
	ID          string   `json:"id"`
	CityID      string   `json:"city_id"`
	Name        string   `json:"name"`
	CountryCode string   `json:"country_code"`
	Tags        []string `json:"tags"`
	Airports    []string `json:"airports"`
	Stations    []string `json:"stations"`
	Description string   `json:"description"`
	Sources     []string `json:"sources"`
}

type Climate struct {
	DestinationID   string   `json:"destination_id"`
	Month           int      `json:"month"`
	TempMaxC        float64  `json:"temp_max_c"`
	TempMinC        float64  `json:"temp_min_c"`
	PrecipitationMM float64  `json:"precipitation_mm"`
	SeaTempC        *float64 `json:"sea_temp_c,omitempty"`
}

type Hotel struct {
	ID            string   `json:"id"`
	DestinationID string   `json:"destination_id"`
	Name          string   `json:"name"`
	Lat           float64  `json:"lat"`
	Lon           float64  `json:"lon"`
	Stars         *int32   `json:"stars,omitempty"`
	CheckInTime   string   `json:"check_in_time,omitempty"`
	CheckOutTime  string   `json:"check_out_time,omitempty"`
	Pets          *bool    `json:"pets,omitempty"`
	OSMID         int64    `json:"osm_id"`
	Sources       []string `json:"sources"`
}

type Route struct {
	FromIATA   string  `json:"from_iata"`
	ToIATA     string  `json:"to_iata"`
	DistanceKm float64 `json:"distance_km"`
	Direct     bool    `json:"direct"`
}

type RailRoute struct {
	FromStation  string   `json:"from_station"`
	ToStation    string   `json:"to_station"`
	DurationMin  int      `json:"duration_min"`
	Departures   []string `json:"departures"`
	TrainNumbers []string `json:"train_numbers"`
}

// Calibration holds the parameters of synthetic prices (CFC-5). All money is
// in roubles; country price levels are multipliers of the Russian level.
type Calibration struct {
	// Price per guest per night by hotel stars, "2".."5", at the Russian level.
	BedPriceRub map[string]float64 `json:"bed_price_rub"`
	// Multiplier of the Russian price level per country code.
	CountryPriceLevel map[string]float64 `json:"country_price_level"`
	// Twelve multipliers, January first.
	SeasonFactor []float64 `json:"season_factor"`
	Airfare      struct {
		BaseRub         float64 `json:"base_rub"`
		RubPerKmShort   float64 `json:"rub_per_km_short"`
		RubPerKmLong    float64 `json:"rub_per_km_long"`
		LongFromKm      float64 `json:"long_from_km"`
		TransferMinutes int     `json:"transfer_minutes"`
	} `json:"airfare"`
	Rail struct {
		RubPerHour float64 `json:"rub_per_hour"`
	} `json:"rail"`
	Transfer struct {
		TaxiBaseRub         float64 `json:"taxi_base_rub"`
		TaxiRubPerKm        float64 `json:"taxi_rub_per_km"`
		PetsSurchargeRub    float64 `json:"pets_surcharge_rub"`
		ShuttleRubPerPerson float64 `json:"shuttle_rub_per_person"`
		PublicRubPerPerson  float64 `json:"public_rub_per_person"`
	} `json:"transfer"`
	// Roubles per unit of currency, from the Bank of Russia at build time.
	FxRub map[string]float64 `json:"fx_rub"`
	// Tourist tax per guest per night by city id, where it applies.
	TouristTaxRubPerNight map[string]float64 `json:"tourist_tax_rub_per_night"`
	Sources               []string           `json:"sources"`
}

// SourceInfo is one entry of the source registry (sources.yaml, CFC-3).
type SourceInfo struct {
	Provider  string    `yaml:"provider"`
	Title     string    `yaml:"title"`
	URL       string    `yaml:"url"`
	License   string    `yaml:"license"`
	Kind      string    `yaml:"kind"`
	Status    string    `yaml:"status"`
	UsedFor   []string  `yaml:"used_for"`
	FetchedAt time.Time `yaml:"fetched_at"`
	Reason    string    `yaml:"reason"`
}

// Data is the whole snapshot with indexes for the providers.
type Data struct {
	Manifest     Manifest
	Sources      []SourceInfo
	Countries    []Country
	Visas        []Visa
	Cities       []City
	Airports     []Airport
	Stations     []Station
	Destinations []Destination
	Climate      []Climate
	Hotels       []Hotel
	Routes       []Route
	RailRoutes   []RailRoute
	Calibration  Calibration

	CountryByCode   map[string]*Country
	CityByID        map[string]*City
	AirportByIATA   map[string]*Airport
	StationByCode   map[string]*Station
	DestinationByID map[string]*Destination
	HotelByID       map[string]*Hotel
	HotelsByDest    map[string][]*Hotel
	ClimateByDest   map[string][12]*Climate
	SourceByName    map[string]*SourceInfo
	visaByPair      map[string]*Visa
	routeByPair     map[string]Route
	railByPair      map[string]RailRoute
}

// Route returns the air route between two airports in either direction.
func (d *Data) Route(from, to string) (Route, bool) {
	r, ok := d.routeByPair[from+"/"+to]
	return r, ok
}

// Rail returns the rail route between two stations in either direction; the
// reverse direction keeps the departures and train numbers of the forward one.
func (d *Data) Rail(from, to string) (RailRoute, bool) {
	r, ok := d.railByPair[from+"/"+to]
	return r, ok
}

// Visa returns the requirement for a passport and a destination country.
func (d *Data) Visa(passport, destination string) (*Visa, bool) {
	v, ok := d.visaByPair[passport+"/"+destination]
	return v, ok
}

// required lists every dataset of phase 1; a snapshot without one is refused.
var required = []string{
	"countries", "visa", "cities", "airports", "stations", "destinations",
	"climate", "hotels", "routes", "rail_routes", "calibration",
}

var (
	reCode       = regexp.MustCompile(`^[A-Z]{2}$`)
	reIATA       = regexp.MustCompile(`^[A-Z]{3}$`)
	reTime       = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)
	sourceKinds  = map[string]bool{"real": true, "synthetic": true, "derived": true}
	visaStatuses = map[string]bool{
		"home": true, "visa_free": true, "visa_on_arrival": true, "eta": true, "e_visa": true,
		"visa_required": true, "no_admission": true,
	}
)

// Load reads and validates the snapshot in dir. Any missing dataset, checksum
// mismatch, malformed line or broken reference is an error: a provider must
// not start on half a snapshot.
func Load(dir string) (*Data, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	d := &Data{}
	if err := json.Unmarshal(raw, &d.Manifest); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	files := map[string]DatasetInfo{}
	for _, ds := range d.Manifest.Datasets {
		files[ds.Name] = ds
	}
	for _, name := range required {
		if _, ok := files[name]; !ok {
			return nil, fmt.Errorf("dataset %q is not in the manifest", name)
		}
	}
	read := func(name string, dst any) error { return readJSONL(dir, files[name], dst) }
	steps := []struct {
		name string
		dst  any
	}{
		{"countries", &d.Countries}, {"visa", &d.Visas}, {"cities", &d.Cities}, {"airports", &d.Airports},
		{"stations", &d.Stations}, {"destinations", &d.Destinations}, {"climate", &d.Climate},
		{"hotels", &d.Hotels}, {"routes", &d.Routes}, {"rail_routes", &d.RailRoutes},
	}
	for _, s := range steps {
		if err := read(s.name, s.dst); err != nil {
			return nil, err
		}
	}
	var cal []Calibration
	if err := read("calibration", &cal); err != nil {
		return nil, err
	}
	if len(cal) != 1 {
		return nil, fmt.Errorf("calibration: want exactly one record, got %d", len(cal))
	}
	d.Calibration = cal[0]
	reg, err := os.ReadFile(filepath.Join(dir, "sources.yaml"))
	if err != nil {
		return nil, fmt.Errorf("read source registry: %w", err)
	}
	if err := yaml.UnmarshalWithOptions(reg, &d.Sources, yaml.DisallowUnknownField()); err != nil {
		return nil, fmt.Errorf("parse sources.yaml: %w", err)
	}
	if err := d.index(); err != nil {
		return nil, err
	}
	return d, nil
}

// readJSONL decodes one dataset file into dst (a pointer to a slice),
// checking the record count and checksum from the manifest.
func readJSONL(dir string, info DatasetInfo, dst any) error {
	raw, err := os.ReadFile(filepath.Join(dir, info.File))
	if err != nil {
		return fmt.Errorf("dataset %q: %w", info.Name, err)
	}
	sum := sha256.Sum256(raw)
	if got := hex.EncodeToString(sum[:]); !strings.EqualFold(got, info.SHA256) {
		return fmt.Errorf("dataset %q: sha256 %s does not match the manifest %s", info.Name, got, info.SHA256)
	}
	var items []json.RawMessage
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 0, 1<<20), 16<<20)
	for line := 1; sc.Scan(); line++ {
		text := bytes.TrimSpace(sc.Bytes())
		if len(text) == 0 {
			continue
		}
		if !json.Valid(text) {
			return fmt.Errorf("dataset %q line %d: invalid JSON", info.Name, line)
		}
		items = append(items, json.RawMessage(bytes.Clone(text)))
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("dataset %q: %w", info.Name, err)
	}
	if len(items) != info.Records {
		return fmt.Errorf("dataset %q: %d records, the manifest says %d", info.Name, len(items), info.Records)
	}
	arr, _ := json.Marshal(items)
	dec := json.NewDecoder(bytes.NewReader(arr))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("dataset %q: %w", info.Name, err)
	}
	return nil
}

func (d *Data) index() error {
	var errs []error
	fail := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }

	d.SourceByName = map[string]*SourceInfo{}
	for i := range d.Sources {
		s := &d.Sources[i]
		if s.Provider == "" || !sourceKinds[s.Kind] || (s.Status != "used" && s.Status != "considered") {
			fail("sources.yaml: bad entry %q (kind %q, status %q)", s.Provider, s.Kind, s.Status)
		}
		d.SourceByName[s.Provider] = s
	}
	known := func(where string, names []string) {
		for _, n := range names {
			if src := d.SourceByName[n]; src == nil || src.Status != "used" {
				fail("%s: source %q is not a used provider in sources.yaml", where, n)
			}
		}
	}

	d.CountryByCode = map[string]*Country{}
	for i := range d.Countries {
		c := &d.Countries[i]
		if !reCode.MatchString(c.Code) || c.NameRu == "" {
			fail("countries: bad record %+v", *c)
		}
		d.CountryByCode[c.Code] = c
	}
	d.visaByPair = map[string]*Visa{}
	for i := range d.Visas {
		v := &d.Visas[i]
		if !reCode.MatchString(v.Passport) || !reCode.MatchString(v.Destination) || !visaStatuses[v.Status] || len(v.Sources) == 0 {
			fail("visa: bad record %+v", *v)
		}
		d.visaByPair[v.Passport+"/"+v.Destination] = v
	}
	d.CityByID = map[string]*City{}
	for i := range d.Cities {
		c := &d.Cities[i]
		if c.ID == "" || d.CountryByCode[c.CountryCode] == nil || c.Timezone == "" {
			fail("cities: bad record %q (country %q)", c.ID, c.CountryCode)
		}
		if _, dup := d.CityByID[c.ID]; dup {
			fail("cities: duplicate id %q", c.ID)
		}
		d.CityByID[c.ID] = c
	}
	d.AirportByIATA = map[string]*Airport{}
	for i := range d.Airports {
		a := &d.Airports[i]
		if !reIATA.MatchString(a.IATA) || d.CityByID[a.CityID] == nil || a.Timezone == "" {
			fail("airports: bad record %q (city %q)", a.IATA, a.CityID)
		}
		d.AirportByIATA[a.IATA] = a
	}
	d.StationByCode = map[string]*Station{}
	for i := range d.Stations {
		s := &d.Stations[i]
		if s.Code == "" || d.CityByID[s.CityID] == nil {
			fail("stations: bad record %q (city %q)", s.Code, s.CityID)
		}
		d.StationByCode[s.Code] = s
	}
	for _, c := range d.Cities {
		for _, a := range c.Airports {
			if d.AirportByIATA[a] == nil {
				fail("cities: %q refers to unknown airport %q", c.ID, a)
			}
		}
		for _, s := range c.Stations {
			if d.StationByCode[s] == nil {
				fail("cities: %q refers to unknown station %q", c.ID, s)
			}
		}
	}
	d.DestinationByID = map[string]*Destination{}
	for i := range d.Destinations {
		x := &d.Destinations[i]
		if x.ID == "" || d.CityByID[x.CityID] == nil || d.CountryByCode[x.CountryCode] == nil || len(x.Sources) == 0 {
			fail("destinations: bad record %q", x.ID)
		}
		if len(x.Airports)+len(x.Stations) == 0 {
			fail("destinations: %q has neither airports nor stations", x.ID)
		}
		d.DestinationByID[x.ID] = x
	}
	d.ClimateByDest = map[string][12]*Climate{}
	for i := range d.Climate {
		c := &d.Climate[i]
		if d.DestinationByID[c.DestinationID] == nil || c.Month < 1 || c.Month > 12 {
			fail("climate: bad record %q month %d", c.DestinationID, c.Month)
			continue
		}
		months := d.ClimateByDest[c.DestinationID]
		months[c.Month-1] = c
		d.ClimateByDest[c.DestinationID] = months
	}
	d.HotelByID = map[string]*Hotel{}
	d.HotelsByDest = map[string][]*Hotel{}
	for i := range d.Hotels {
		h := &d.Hotels[i]
		if h.ID == "" || h.Name == "" || d.DestinationByID[h.DestinationID] == nil || len(h.Sources) == 0 {
			fail("hotels: bad record %q", h.ID)
		}
		if (h.CheckInTime != "" && !reTime.MatchString(h.CheckInTime)) || (h.CheckOutTime != "" && !reTime.MatchString(h.CheckOutTime)) {
			fail("hotels: %q check-in/out time must be HH:MM", h.ID)
		}
		if _, dup := d.HotelByID[h.ID]; dup {
			fail("hotels: duplicate id %q", h.ID)
		}
		d.HotelByID[h.ID] = h
		d.HotelsByDest[h.DestinationID] = append(d.HotelsByDest[h.DestinationID], h)
	}
	d.routeByPair = map[string]Route{}
	for _, r := range d.Routes {
		if d.AirportByIATA[r.FromIATA] == nil || d.AirportByIATA[r.ToIATA] == nil || r.DistanceKm <= 0 {
			fail("routes: bad record %s-%s", r.FromIATA, r.ToIATA)
		}
		d.routeByPair[r.FromIATA+"/"+r.ToIATA] = r
	}
	for _, r := range d.Routes {
		if _, ok := d.routeByPair[r.ToIATA+"/"+r.FromIATA]; !ok {
			d.routeByPair[r.ToIATA+"/"+r.FromIATA] = Route{FromIATA: r.ToIATA, ToIATA: r.FromIATA, DistanceKm: r.DistanceKm, Direct: r.Direct}
		}
	}
	d.railByPair = map[string]RailRoute{}
	for _, r := range d.RailRoutes {
		d.railByPair[r.FromStation+"/"+r.ToStation] = r
	}
	for _, r := range d.RailRoutes {
		if _, ok := d.railByPair[r.ToStation+"/"+r.FromStation]; !ok {
			d.railByPair[r.ToStation+"/"+r.FromStation] = RailRoute{FromStation: r.ToStation, ToStation: r.FromStation,
				DurationMin: r.DurationMin, Departures: r.Departures, TrainNumbers: r.TrainNumbers}
		}
	}
	for _, r := range d.RailRoutes {
		if d.StationByCode[r.FromStation] == nil || d.StationByCode[r.ToStation] == nil || r.DurationMin <= 0 {
			fail("rail_routes: bad record %s-%s", r.FromStation, r.ToStation)
		}
		if len(r.Departures) == 0 || len(r.Departures) != len(r.TrainNumbers) {
			fail("rail_routes: %s-%s needs one train number per departure", r.FromStation, r.ToStation)
		}
		for _, t := range r.Departures {
			if !reTime.MatchString(t) {
				fail("rail_routes: %s-%s departure %q must be HH:MM", r.FromStation, r.ToStation, t)
			}
		}
	}
	cal := d.Calibration
	if len(cal.SeasonFactor) != 12 {
		fail("calibration: season_factor needs 12 values, got %d", len(cal.SeasonFactor))
	}
	for _, stars := range []string{"2", "3", "4", "5"} {
		if cal.BedPriceRub[stars] <= 0 {
			fail("calibration: bed_price_rub[%s] must be positive", stars)
		}
	}
	for _, x := range d.Destinations {
		if cal.CountryPriceLevel[x.CountryCode] <= 0 {
			fail("calibration: no country_price_level for %s (destination %q)", x.CountryCode, x.ID)
		}
	}
	for _, v := range d.Visas {
		known("visa "+v.Passport+"/"+v.Destination, v.Sources)
	}
	for _, x := range d.Destinations {
		known("destination "+x.ID, x.Sources)
	}
	for _, h := range d.Hotels {
		known("hotel "+h.ID, h.Sources)
	}
	known("calibration", d.Calibration.Sources)
	return errors.Join(errs...)
}

// Fold makes names comparable: trimmed, lower case, ё as е.
func Fold(s string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(s)), "ё", "е")
}

// FindCity resolves a city by its id, Russian or English name, or by a
// destination id or name (the destination's city).
func (d *Data) FindCity(name string) *City {
	q := Fold(name)
	if c := d.CityByID[q]; c != nil {
		return c
	}
	for i := range d.Cities {
		if c := &d.Cities[i]; Fold(c.NameRu) == q || Fold(c.NameEn) == q {
			return c
		}
	}
	for _, x := range d.Destinations {
		if Fold(x.ID) == q || Fold(x.Name) == q {
			return d.CityByID[x.CityID]
		}
	}
	return nil
}
