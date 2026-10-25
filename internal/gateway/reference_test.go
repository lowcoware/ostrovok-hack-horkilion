package gateway

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	catalogv1 "horkilion/gen/catalog/v1"
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
