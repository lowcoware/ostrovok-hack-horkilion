package gateway

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	catalogv1 "horkilion/gen/catalog/v1"
	transferv1 "horkilion/gen/transfer/v1"
	transportv1 "horkilion/gen/transport/v1"
	"horkilion/internal/agent/plan"
	"horkilion/internal/catalog"
	"horkilion/internal/dataset"
)

// catalogClient serves the real catalog provider on the test snapshot over an
// in-memory gRPC connection.
func catalogClient(t *testing.T, data *dataset.Data) catalogv1.CatalogServiceClient {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	catalogv1.RegisterCatalogServiceServer(srv, catalog.NewOpenData(data))
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet", grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return catalogv1.NewCatalogServiceClient(conn)
}

func TestCountriesStartWithRussia(t *testing.T) {
	_, _, _, newClient := setup(t)
	rec := newClient().do(http.MethodGet, "/api/v1/countries", "", nil)
	var body struct {
		Countries []country `json:"countries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("GET /countries: %d %s", rec.Code, rec.Body)
	}
	if len(body.Countries) == 0 || body.Countries[0].Code != "RU" || body.Countries[0].Name != "Россия" {
		t.Errorf("want Россия first, got %+v", body.Countries)
	}
}

func TestPlaces(t *testing.T) {
	_, _, _, newClient := setup(t)
	c := newClient()
	for path, want := range map[string]int{
		"/api/v1/places?query=%D0%BC%D0%B8%D0%BD":           http.StatusOK,         // «мин»
		"/api/v1/places?query=%D0%BC":                       http.StatusBadRequest, // «м»
		"/api/v1/places":                                    http.StatusBadRequest,
		"/api/v1/places?query=%D0%BC%D0%B8%D0%BD&kind=city": http.StatusOK,
	} {
		if rec := c.do(http.MethodGet, path, "", nil); rec.Code != want {
			t.Errorf("GET %s: want %d, got %d %s", path, want, rec.Code, rec.Body)
		}
	}
	rec := c.do(http.MethodGet, "/api/v1/places?query=%D0%BC%D0%B8%D0%BD", "", nil)
	var body struct {
		Places []struct{ Name string } `json:"places"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if len(body.Places) == 0 || body.Places[0].Name != "Минск" {
		t.Errorf("«мин» must suggest Минск, got %s", rec.Body)
	}
}

func TestSourcesComeFromTheRegistry(t *testing.T) {
	_, _, _, newClient := setup(t)
	rec := newClient().do(http.MethodGet, "/api/v1/sources", "", nil)
	var body struct {
		Providers []Provider `json:"providers"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	names := map[string]bool{}
	for _, p := range body.Providers {
		names[p.Provider] = true
	}
	if !names["passport-index-data"] || !names["osm"] || !names["synthetic"] {
		t.Errorf("want the used providers of the snapshot, got %v", names)
	}
}

func TestProvidersSkipUnusedEntries(t *testing.T) {
	got := Providers([]dataset.SourceInfo{
		{Provider: "osm", Title: "OpenStreetMap", Kind: "real", Status: "used", UsedFor: []string{"отели"}},
		{Provider: "amadeus", Title: "Amadeus", Kind: "real", Status: "rejected", Reason: "нет бесплатного доступа"},
	})
	if len(got) != 1 || got[0].Provider != "osm" || got[0].Description == "" {
		t.Errorf("want only the used osm entry with a description, got %+v", got)
	}
}

func TestCartIsTheAssembledTrip(t *testing.T) {
	p := searchedPlan(uuid.NewString())
	journey := func(id string) plan.Option {
		return plan.JourneyOption(&transportv1.Journey{Id: id, PriceRub: 12000, Segments: []*transportv1.Segment{{Carrier: "SU", Number: id}}})
	}
	transfer := func(id string) plan.Option {
		return plan.TransferOption(&transferv1.Transfer{Id: id, PriceRub: 1500})
	}
	p.SetOptions(plan.Outbound, []plan.Option{journey("out1"), journey("out2")})
	p.SetOptions(plan.Return, []plan.Option{journey("back1")})
	p.SetOptions(plan.TransferArrival, []plan.Option{transfer("in1")})
	p.SetOptions(plan.TransferDeparture, []plan.Option{transfer("dep1")})

	items, err := cartItems(p)
	if err != nil || len(items) != 5 {
		t.Fatalf("the hotel and the four assembled components must be in the cart, got %d items, %v", len(items), err)
	}
	var sum int64
	for _, it := range items {
		sum += it.GetPriceRub()
	}
	if sum != p.TotalRub || items[0].GetOfferId() != "out1" {
		t.Errorf("total %d must be the cart sum %d, first outbound by default, got %s", p.TotalRub, sum, items[0].GetOfferId())
	}

	p.Components.Stay.SelectedID = ""
	if _, err := cartItems(p); err == nil || err.Error() != "nothing_selected" {
		t.Errorf("without a chosen hotel there is nothing to buy, got %v", err)
	}
}
