package ride

import (
	"strings"
	"testing"

	"github.com/Matheusmottaa/nearby-drivers/internal/event"
)

func TestMetricsRecordChosenDriver(t *testing.T) {
	body := []byte(`{"passenger_id":"` + passenger + `","pickup":{"lat":-23.5613,"lng":-46.6565},"vehicle_class":"standard"}`)
	req := Request{PassengerID: passenger, PickupLat: -23.5613, PickupLng: -46.6565, VehicleClass: "standard", TraceID: "req-abc123"}
	svc, _, _ := newTestService(t, 3)
	ride := mustMatching(t, svc, "k-metrics", body, req)
	if err := svc.ApplyMatched(t.Context(), matched(ride, 1, driver1, driver2)); err != nil {
		t.Fatal(err)
	}

	counts, err := svc.CountByStatus(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var buf strings.Builder
	if err := WriteMetrics(&buf, svc.Snapshot(), counts); err != nil {
		t.Fatal(err)
	}
	text := buf.String()
	for _, want := range []string{
		`ride_assigned_total{driver_id="` + driver1 + `",engine="` + event.EngineDijkstra + `"} 1`,
		`ride_assignment_eta_seconds{seq="1",ride_id="` + ride.ID + `",driver_id="` + driver1 + `",engine="` + event.EngineDijkstra + `"} 142`,
		`ride_requests{status="DRIVER_ASSIGNED"} 1`,
		`ride_requests{status="REQUESTED"} 0`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("metrics missing %s\n%s", want, text)
		}
	}
	if strings.Contains(text, driver2) {
		t.Fatalf("second-ranked driver was recorded\n%s", text)
	}
}
