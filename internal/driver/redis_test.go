package driver

import (
	"encoding/json"
	"testing"
	"time"
)

func TestLastPositionKey(t *testing.T) {
	if got := lastKey("abc"); got != "driver:abc:last" {
		t.Fatalf("key = %q", got)
	}
	if LastPositionTTL != 60*time.Second {
		t.Fatalf("ttl = %s", LastPositionTTL)
	}
	raw, err := json.Marshal(lastPosition{
		Lat:       -23.56,
		Lng:       -46.65,
		Seq:       7,
		Timestamp: time.Date(2026, 9, 30, 18, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"lat", "lng", "seq", "timestamp"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("missing %s in %s", key, raw)
		}
	}
}

func TestReplaySpan(t *testing.T) {
	if replaySpan(0, 0) || replaySpan(-1, 10) || replaySpan(10, 10) || replaySpan(11, 10) {
		t.Fatal("empty windows must not be read")
	}
	if !replaySpan(0, 10) || !replaySpan(4, 5) {
		t.Fatal("a start before the end offset has records")
	}
}
