package agent

import (
	"context"
	"net"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	catalogv1 "horkilion/gen/catalog/v1"
	lodgingv1 "horkilion/gen/lodging/v1"
	transferv1 "horkilion/gen/transfer/v1"
	transportv1 "horkilion/gen/transport/v1"
	"horkilion/internal/agent/plan"
	"horkilion/internal/apitest"
	"horkilion/internal/catalog"
	"horkilion/internal/dataset"
	"horkilion/internal/lodging"
	"horkilion/internal/transfer"
	"horkilion/internal/transport"
)

// inventoryAgent is an agent wired to the real data-backed providers on the
// test snapshot over an in-memory gRPC connection.
func inventoryAgent(t *testing.T) *Agent {
	t.Helper()
	data, err := dataset.Load("../dataset/testdata/snapshot")
	if err != nil {
		t.Fatal(err)
	}
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	lodgingv1.RegisterLodgingServiceServer(srv, lodging.NewOSM(data))
	catalogv1.RegisterCatalogServiceServer(srv, offlineCatalog{catalog.NewOpenData(data)})
	transportv1.RegisterTransportServiceServer(srv, transport.NewSynthetic(data))
	transferv1.RegisterTransferServiceServer(srv, transfer.NewSynthetic(data))
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet", grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })

	var rules Rules
	rules.Connections.MinArrivalToCheckinMinutes, rules.Connections.DepartureBufferMinutes = 60, 120
	return New(nil, nil, rules, Suppliers{
		Lodging: lodgingv1.NewLodgingServiceClient(conn), Catalog: catalogv1.NewCatalogServiceClient(conn),
		Transport: transportv1.NewTransportServiceClient(conn), Transfer: transferv1.NewTransferServiceClient(conn),
	}, nil, nil)
}

// offlineCatalog keeps tests off the network: forecasts come from Open-Meteo.
type offlineCatalog struct{ *catalog.OpenData }

func (offlineCatalog) GetWeather(context.Context, *catalogv1.GetWeatherRequest) (*catalogv1.Weather, error) {
	return &catalogv1.Weather{TempMaxC: 18, TempMinC: 10, Kind: catalogv1.Weather_KIND_CLIMATE_NORMAL}, nil
}

// say routes a message by rules and runs the turn the way HandleTurn does.
func say(t *testing.T, a *Agent, p *plan.TripPlan, text string) {
	t.Helper()
	p.Apply(routeRules(*p, text, today).patch(), today)
	if err := a.advance(context.Background(), p, &events{}, today); err != nil {
		t.Fatal(err)
	}
}

// turn applies a patch the way HandleTurn does and assembles the trip.
func turn(t *testing.T, a *Agent, p *plan.TripPlan, patch plan.Patch) {
	t.Helper()
	p.Apply(patch, today)
	p.FillDefaults(today)
	if err := a.assemble(context.Background(), p, &events{}); err != nil {
		t.Fatal(err)
	}
}

func statusOf(p plan.TripPlan, kind string) string {
	for _, c := range p.Checks {
		if c.Kind == kind {
			return c.Status
		}
	}
	return ""
}

func TestAssembleFullTrip(t *testing.T) {
	a := inventoryAgent(t)
	p := plan.New("s")
	turn(t, a, &p, plan.Patch{Destination: ptr("Сочи"), Dates: &plan.DatesPatch{Depart: ptr("2026-11-20")}})

	for _, name := range plan.ComponentNames {
		if p.Components.Get(name).Effective() == nil {
			t.Errorf("%s has no options", name)
		}
	}
	for _, kind := range []string{plan.CheckVisa, plan.CheckArrivalBeforeCheckin, plan.CheckTransferFits, plan.CheckCheckoutBeforeDeparture, plan.CheckDatesConsistent} {
		if statusOf(p, kind) == "" {
			t.Errorf("no %s check: %+v", kind, p.Checks)
		}
	}
	if statusOf(p, plan.CheckVisa) != "ok" {
		t.Errorf("Сочи is home for a Russian passport, got %s", statusOf(p, plan.CheckVisa))
	}
	if statusOf(p, plan.CheckDatesConsistent) != "ok" {
		t.Errorf("the default outbound must arrive on the check-in day while one does: %+v", p.Checks)
	}
	if statusOf(p, plan.CheckTransferFits) != "ok" {
		t.Errorf("the departure transfer is timed for the buffer and must fit: %+v", p.Checks)
	}
	apitest.ValidateSchema(t, "TripPlan", p)
	if got := p.Components.Return.Effective().Journey.Segments[0].DepartAt; !strings.HasPrefix(got, "2026-11-23") {
		t.Errorf("the return must leave on the return date, got %s", got)
	}

	// «на два дня позже»: every component is re-checked on the new dates, the destination stays.
	turn(t, a, &p, plan.Patch{Dates: &plan.DatesPatch{Depart: ptr("2026-11-22")}})
	if p.Constraints.Destination != "Сочи" {
		t.Errorf("a date shift must keep the destination, got %s", p.Constraints.Destination)
	}
	if got := p.Components.Outbound.Effective().Journey.Segments[0].DepartAt; !strings.HasPrefix(got, "2026-11-22") {
		t.Errorf("the outbound must move to the new date, got %s", got)
	}
	if got := p.Components.TransferDeparture.Effective().Transfer.DepartAt; !strings.HasPrefix(got, "2026-11-25") {
		t.Errorf("the departure transfer must move with the return, got %s", got)
	}
}

func TestNamedVisaDestinationFails(t *testing.T) {
	a := inventoryAgent(t)
	p := plan.New("s")
	turn(t, a, &p, plan.Patch{Destination: ptr("Париж")})
	if p.Visa == nil || p.Visa.Status != plan.VisaRequired || statusOf(p, plan.CheckVisa) != "fail" {
		t.Errorf("Paris for a Russian passport must fail the visa check, got %+v %+v", p.Visa, p.Checks)
	}

	turn(t, a, &p, plan.Patch{Citizenship: ptr("FR")})
	if p.Visa == nil || p.Visa.Status != plan.VisaHome {
		t.Errorf("a French passport is at home in Paris, got %+v", p.Visa)
	}
}
