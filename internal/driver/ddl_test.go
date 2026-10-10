package driver

import (
	"strings"
	"testing"
)

func TestDDLRunsAsDriverUser(t *testing.T) {
	for _, part := range []string{
		"SET ROLE driver;",
		"current_user <> 'driver'",
		"CREATE TABLE drivers",
	} {
		if !strings.Contains(ddl, part) {
			t.Fatalf("driver DDL missing %q", part)
		}
	}
	for _, part := range []string{
		"SET ROLE driver;",
		"CREATE TABLE idempotency_keys",
	} {
		if !strings.Contains(idempotencyDDL, part) {
			t.Fatalf("idempotency DDL missing %q", part)
		}
	}
}

func TestDDLHasNoPositionColumn(t *testing.T) {
	body := ddl
	if i := strings.Index(body, "CREATE TABLE drivers"); i >= 0 {
		body = body[i:]
	}
	if j := strings.Index(body, ");"); j >= 0 {
		body = body[:j]
	}
	lower := strings.ToLower(body)
	for _, col := range []string{" lat ", " lng ", "heartbeat", "accuracy"} {
		if strings.Contains(lower, col) {
			t.Fatalf("drivers table stores %s; position stays in the memory slot", col)
		}
	}
}
