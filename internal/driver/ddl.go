package driver

import (
	"context"
	_ "embed"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Matheusmottaa/nearby-drivers/internal/schema"
)

//go:embed sql/001_tables.sql
var ddl string

// ApplyDDL creates the driver schema table as the driver user.
// The script refuses to run when current_user is not driver.
func ApplyDDL(ctx context.Context, pool *pgxpool.Pool) error {
	return schema.Apply(ctx, pool, "driver.drivers", ddl)
}
