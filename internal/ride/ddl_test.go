package ride

import (
	"strings"
	"testing"
)

func TestDDLRunsAsRideUser(t *testing.T) {
	for _, part := range []string{
		"SET ROLE ride;",
		"current_user <> 'ride'",
		"CREATE TABLE rides",
		"CREATE TABLE outbox",
		"CREATE TABLE idempotency_keys",
	} {
		if !strings.Contains(ddl, part) {
			t.Fatalf("ride DDL missing %q", part)
		}
	}
}
