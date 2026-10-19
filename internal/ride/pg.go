package ride

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Matheusmottaa/nearby-drivers/internal/event"
)

const rideColumns = `
	id::text, passenger_id::text, status, pickup_lat, pickup_lng, vehicle_class,
	COALESCE(assigned_driver_id::text, ''),
	assigned_eta_s, assigned_distance_m, COALESCE(eta_engine, ''),
	COALESCE(excluded_driver_ids::text[], '{}'),
	attempt, COALESCE(cancel_reason, ''), version, requested_at, updated_at,
	COALESCE((
		SELECT payload->>'trace_id' FROM outbox
		WHERE aggregate_id = rides.id AND subject = 'rides.requested'
		ORDER BY id LIMIT 1
	), '')`

// Postgres is the ride schema. The pool user is ride.
type Postgres struct {
	pool *pgxpool.Pool
}

// NewPostgres returns a store that uses pool.
func NewPostgres(pool *pgxpool.Pool) *Postgres {
	return &Postgres{pool: pool}
}

func (p *Postgres) InsertNew(ctx context.Context, key string, hash []byte, ride Ride, ev OutboxEvent) (Ride, bool, error) {
	var last error
	for range 2 {
		stored, created, err := p.insert(ctx, key, hash, ride, ev)
		if errors.Is(err, errIdemRace) {
			last = err
			continue
		}
		if err != nil {
			return Ride{}, false, err
		}
		return stored, created, nil
	}
	stored, same, err := p.byKey(ctx, key, hash)
	if err != nil {
		if last != nil {
			return Ride{}, false, last
		}
		return Ride{}, false, err
	}
	return stored, same, nil
}

var errIdemRace = errors.New("idempotency key race")

func (p *Postgres) insert(ctx context.Context, key string, hash []byte, ride Ride, ev OutboxEvent) (stored Ride, created bool, err error) {
	err = pgx.BeginFunc(ctx, p.pool, func(tx pgx.Tx) error {
		var existingHash []byte
		var existingID string
		qerr := tx.QueryRow(ctx, `SELECT body_hash, ride_id::text FROM idempotency_keys WHERE key = $1`, key).
			Scan(&existingHash, &existingID)
		if qerr == nil {
			if !bytes.Equal(existingHash, hash) {
				return ErrIdempotencyConflict
			}
			stored, err = getTx(ctx, tx, existingID)
			return err
		}
		if !errors.Is(qerr, pgx.ErrNoRows) {
			return qerr
		}
		if _, err = tx.Exec(ctx, `
			INSERT INTO rides (
				id, passenger_id, status, pickup_lat, pickup_lng, vehicle_class, attempt, version
			) VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7, $8)`,
			ride.ID, ride.PassengerID, ride.Status, ride.PickupLat, ride.PickupLng,
			ride.VehicleClass, ride.Attempt, ride.Version,
		); err != nil {
			return err
		}
		if err = insertOutbox(ctx, tx, ride.ID, ev); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `
			INSERT INTO idempotency_keys (key, body_hash, ride_id) VALUES ($1, $2, $3::uuid)`,
			key, hash, ride.ID,
		); err != nil {
			if isUnique(err, "idempotency_keys_pkey") {
				return errIdemRace
			}
			return err
		}
		stored, err = getTx(ctx, tx, ride.ID)
		created = err == nil
		return err
	})
	return stored, created, err
}

func (p *Postgres) byKey(ctx context.Context, key string, hash []byte) (Ride, bool, error) {
	var existingHash []byte
	var id string
	err := p.pool.QueryRow(ctx, `SELECT body_hash, ride_id::text FROM idempotency_keys WHERE key = $1`, key).
		Scan(&existingHash, &id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Ride{}, false, ErrNotFound
	}
	if err != nil {
		return Ride{}, false, err
	}
	if !bytes.Equal(existingHash, hash) {
		return Ride{}, false, ErrIdempotencyConflict
	}
	ride, err := p.Get(ctx, id)
	return ride, false, err
}

func (p *Postgres) Get(ctx context.Context, id string) (Ride, error) {
	if !validUUID(id) {
		return Ride{}, ErrNotFound
	}
	ride, err := getTx(ctx, p.pool, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Ride{}, ErrNotFound
	}
	if err != nil {
		return Ride{}, fmt.Errorf("get ride %s: %w", id, err)
	}
	return ride, nil
}

type queryer interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func getTx(ctx context.Context, q queryer, id string) (Ride, error) {
	var ride Ride
	var eta, dist *int32
	err := q.QueryRow(ctx, `SELECT `+rideColumns+` FROM rides WHERE id = $1::uuid`, id).Scan(
		&ride.ID, &ride.PassengerID, &ride.Status, &ride.PickupLat, &ride.PickupLng, &ride.VehicleClass,
		&ride.AssignedDriverID, &eta, &dist, &ride.ETAEngine, &ride.ExcludedDriverIDs,
		&ride.Attempt, &ride.CancelReason, &ride.Version, &ride.RequestedAt, &ride.UpdatedAt, &ride.TraceID,
	)
	if err != nil {
		return Ride{}, err
	}
	if eta != nil {
		ride.AssignedETAS = int(*eta)
	}
	if dist != nil {
		ride.AssignedDistanceM = int(*dist)
	}
	if ride.ExcludedDriverIDs == nil {
		ride.ExcludedDriverIDs = []string{}
	}
	return ride, nil
}

func (p *Postgres) Commit(ctx context.Context, fromVersion int, next Ride, events []OutboxEvent) error {
	return pgx.BeginFunc(ctx, p.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE rides SET
				status = $2,
				assigned_driver_id = NULLIF($3, '')::uuid,
				assigned_eta_s = $4,
				assigned_distance_m = $5,
				eta_engine = NULLIF($6, ''),
				excluded_driver_ids = $7::text[]::uuid[],
				attempt = $8,
				cancel_reason = NULLIF($9, ''),
				version = version + 1,
				updated_at = now()
			WHERE id = $1::uuid AND version = $10`,
			next.ID, next.Status, next.AssignedDriverID, nullableInt(next.Status == StatusAssigned, next.AssignedETAS),
			nullableInt(next.Status == StatusAssigned, next.AssignedDistanceM), next.ETAEngine,
			textArray(next.ExcludedDriverIDs), next.Attempt, next.CancelReason, fromVersion,
		)
		if err != nil {
			if isUnique(err, "rides_assigned_driver_id_active_uidx") {
				return ErrDriverTaken
			}
			return fmt.Errorf("update ride %s: %w", next.ID, err)
		}
		if tag.RowsAffected() == 0 {
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT true FROM rides WHERE id = $1::uuid`, next.ID).Scan(&exists); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return ErrNotFound
				}
				return err
			}
			return ErrVersionConflict
		}
		for _, ev := range events {
			if err := insertOutbox(ctx, tx, next.ID, ev); err != nil {
				return err
			}
		}
		return nil
	})
}

func (p *Postgres) SaveRanking(ctx context.Context, m event.Matched) error {
	ranking := m.Ranking
	if ranking == nil {
		ranking = []event.Candidate{}
	}
	raw, err := event.Encode(ranking)
	if err != nil {
		return err
	}
	_, err = p.pool.Exec(ctx, `
		INSERT INTO rankings (ride_id, attempt, event_id, engine, candidates)
		VALUES ($1::uuid, $2, $3, $4, $5::jsonb)
		ON CONFLICT (ride_id, attempt) DO UPDATE SET
			event_id = EXCLUDED.event_id,
			engine = EXCLUDED.engine,
			candidates = EXCLUDED.candidates,
			received_at = now()`,
		m.RideID, m.Attempt, m.EventID, m.Engine, raw,
	)
	if err != nil {
		return fmt.Errorf("save ranking %s attempt %d: %w", m.RideID, m.Attempt, err)
	}
	return nil
}

func nullableInt(set bool, n int) any {
	if !set {
		return nil
	}
	return n
}

func textArray(ids []string) []string {
	if ids == nil {
		return []string{}
	}
	return ids
}

func insertOutbox(ctx context.Context, tx pgx.Tx, rideID string, ev OutboxEvent) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO outbox (event_id, aggregate_id, subject, payload)
		VALUES ($1::uuid, $2::uuid, $3, $4::jsonb)`,
		ev.EventID, rideID, ev.Subject, ev.Payload,
	)
	if err != nil {
		return fmt.Errorf("insert outbox %s: %w", ev.Subject, err)
	}
	return nil
}

func (p *Postgres) PublishPending(ctx context.Context, limit int, publish func(PendingPublish) error) ([]string, error) {
	var became []string
	err := pgx.BeginFunc(ctx, p.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, event_id::text, aggregate_id::text, subject, payload, created_at
			FROM outbox
			WHERE published_at IS NULL
			ORDER BY id
			LIMIT $1
			FOR UPDATE SKIP LOCKED`, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		type row struct {
			id      int64
			pending PendingPublish
		}
		var batch []row
		for rows.Next() {
			var item row
			if err := rows.Scan(&item.id, &item.pending.EventID, &item.pending.RideID, &item.pending.Subject, &item.pending.Payload, &item.pending.CreatedAt); err != nil {
				return err
			}
			batch = append(batch, item)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		rows.Close()
		for _, item := range batch {
			if err := publish(item.pending); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE outbox SET published_at = now() WHERE id = $1`, item.id); err != nil {
				return err
			}
			if item.pending.Subject != event.TopicRidesRequested {
				continue
			}
			tag, err := tx.Exec(ctx, `
				UPDATE rides
				SET status = $2, version = version + 1, updated_at = now()
				WHERE id = $1::uuid AND status = $3`,
				item.pending.RideID, StatusMatching, StatusRequested,
			)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 1 {
				became = append(became, item.pending.RideID)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return became, nil
}

func (p *Postgres) CountByStatus(ctx context.Context) (map[string]int, error) {
	rows, err := p.pool.Query(ctx, `SELECT status, COUNT(*)::int FROM rides GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := make(map[string]int)
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			return nil, err
		}
		counts[status] = n
	}
	return counts, rows.Err()
}

func (p *Postgres) OutboxStats(ctx context.Context) (int, time.Duration, error) {
	var pending int
	var lagMS float64
	err := p.pool.QueryRow(ctx, `
		SELECT COUNT(*)::int,
		       COALESCE(EXTRACT(EPOCH FROM (now() - MIN(created_at))) * 1000, 0)
		FROM outbox
		WHERE published_at IS NULL`).Scan(&pending, &lagMS)
	if err != nil {
		return 0, 0, err
	}
	return pending, time.Duration(lagMS) * time.Millisecond, nil
}

func isUnique(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == constraint
}
