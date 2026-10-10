// Package schema applies a service's versioned DDL once, on a connection that
// already belongs to the schema owner.
package schema

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Apply runs script inside a transaction when relation does not exist yet.
// relation is a schema-qualified name passed to to_regclass, for example ride.rides.
// A second boot sees the relation and does nothing, so CREATE TABLE stays exact.
func Apply(ctx context.Context, pool *pgxpool.Pool, relation, script string) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire postgres: %w", err)
	}
	defer conn.Release()

	var exists bool
	if err := conn.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, relation).Scan(&exists); err != nil {
		return fmt.Errorf("check %s: %w", relation, err)
	}
	if exists {
		return nil
	}

	batch := "BEGIN;\n" + script + "\nCOMMIT;\n"
	if err := conn.Conn().PgConn().Exec(ctx, batch).Close(); err != nil {
		_, _ = conn.Exec(ctx, "ROLLBACK")
		return fmt.Errorf("apply %s: %w", relation, err)
	}
	return nil
}
