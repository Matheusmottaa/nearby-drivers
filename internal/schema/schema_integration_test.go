//go:build integration

package schema_test

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Matheusmottaa/nearby-drivers/internal/depend"
	"github.com/Matheusmottaa/nearby-drivers/internal/driver"
	"github.com/Matheusmottaa/nearby-drivers/internal/ride"
)

func TestApplyDDLTwice(t *testing.T) {
	t.Run("ride", func(t *testing.T) {
		applyTwice(t, os.Getenv("RIDE_DATABASE_URL"), ride.ApplyDDL)
	})
	t.Run("driver", func(t *testing.T) {
		applyTwice(t, os.Getenv("DRIVER_DATABASE_URL"), driver.ApplyDDL)
	})
}

func applyTwice(t *testing.T, url string, apply func(context.Context, *pgxpool.Pool) error) {
	t.Helper()
	if url == "" {
		t.Skip("database url not set")
	}
	pool, err := depend.OpenPostgres(t.Context(), url)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	t.Cleanup(pool.Close)

	if err := apply(t.Context(), pool); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := apply(t.Context(), pool); err != nil {
		t.Fatalf("apply again: %v", err)
	}
}
