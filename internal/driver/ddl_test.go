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
}
