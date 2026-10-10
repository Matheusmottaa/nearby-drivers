package driver

import (
	"context"
	_ "embed"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Matheusmottaa/nearby-drivers/internal/schema"
)

//go:embed sql/001_tables.sql
var ddl string

//go:embed sql/002_idempotency.sql
var idempotencyDDL string

// ApplyDDL creates the driver tables as the driver user.
// The scripts refuse to run when current_user is not driver.
// A second boot sees the relations and does nothing.
func ApplyDDL(ctx context.Context, pool *pgxpool.Pool) error {
	if err := schema.Apply(ctx, pool, "driver.drivers", ddl); err != nil {
		return err
	}
	return schema.Apply(ctx, pool, "driver.idempotency_keys", idempotencyDDL)
}
