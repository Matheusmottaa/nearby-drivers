package ride

import (
	"context"
	_ "embed"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Matheusmottaa/nearby-drivers/internal/schema"
)

//go:embed sql/001_tables.sql
var ddl string

//go:embed sql/002_rankings.sql
var rankingsDDL string

// ApplyDDL creates the ride schema tables as the ride user.
// The scripts refuse to run when current_user is not ride.
// A second boot sees the relations and does nothing.
func ApplyDDL(ctx context.Context, pool *pgxpool.Pool) error {
	if err := schema.Apply(ctx, pool, "ride.rides", ddl); err != nil {
		return err
	}
	return schema.Apply(ctx, pool, "ride.rankings", rankingsDDL)
}
