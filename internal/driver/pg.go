package driver

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	driverColumns = `id::text, internal_id, name, phone, vehicle_class, plate, status, COALESCE(current_ride_id::text, ''), registered_at`
)

// Postgres is the driver schema.
type Postgres struct {
	pool *pgxpool.Pool
}

// NewPostgres returns a store that uses pool. The pool user is driver.
func NewPostgres(pool *pgxpool.Pool) *Postgres {
	return &Postgres{pool: pool}
}

func (p *Postgres) Create(ctx context.Context, key string, in Registration) (Driver, bool, error) {
	var last error
	for range 2 {
		d, created, err := p.insert(ctx, key, in)
		if errors.Is(err, errIdemRace) {
			last = err
			continue
		}
		if err != nil {
			return Driver{}, false, err
		}
		return d, created, nil
	}
	d, err := p.byKey(ctx, key)
	if err != nil {
		if last != nil {
			return Driver{}, false, last
		}
		return Driver{}, false, err
	}
	return d, false, nil
}

var errIdemRace = errors.New("idempotency key race")

func (p *Postgres) insert(ctx context.Context, key string, in Registration) (d Driver, created bool, err error) {
	id, err := newID()
	if err != nil {
		return Driver{}, false, err
	}
	err = pgx.BeginFunc(ctx, p.pool, func(tx pgx.Tx) error {
		var existing string
		qerr := tx.QueryRow(ctx, `SELECT driver_id::text FROM idempotency_keys WHERE key = $1`, key).Scan(&existing)
		if qerr == nil {
			d, err = getTx(ctx, tx, existing)
			return err
		}
		if !errors.Is(qerr, pgx.ErrNoRows) {
			return qerr
		}
		d, err = insertDriver(ctx, tx, id, in)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO idempotency_keys (key, driver_id) VALUES ($1, $2::uuid)`, key, id); err != nil {
			if isUnique(err, "idempotency_keys_pkey") {
				return errIdemRace
			}
			return err
		}
		created = true
		return nil
	})
	return d, created, err
}

func (p *Postgres) byKey(ctx context.Context, key string) (Driver, error) {
	var id string
	err := p.pool.QueryRow(ctx, `SELECT driver_id::text FROM idempotency_keys WHERE key = $1`, key).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Driver{}, ErrNotFound
	}
	if err != nil {
		return Driver{}, err
	}
	return p.Get(ctx, id)
}

func (p *Postgres) Get(ctx context.Context, id string) (Driver, error) {
	if !validUUID(id) {
		return Driver{}, ErrNotFound
	}
	d, err := getTx(ctx, p.pool, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Driver{}, ErrNotFound
	}
	return d, err
}

func (p *Postgres) UpdateStatus(ctx context.Context, id, status string) error {
	if !validUUID(id) {
		return ErrNotFound
	}
	tag, err := p.pool.Exec(ctx, `
		UPDATE drivers
		SET status = $2
		WHERE id = $1::uuid AND status <> 'ON_TRIP'`, id, status)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	var current string
	err = p.pool.QueryRow(ctx, `SELECT status FROM drivers WHERE id = $1::uuid`, id).Scan(&current)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if current == StatusOnTrip {
		return ErrIllegalStatus
	}
	return nil
}

func (p *Postgres) Assign(ctx context.Context, driverID, rideID string) (AssignResult, error) {
	if !validUUID(driverID) || !validUUID(rideID) {
		return AssignMissing, nil
	}
	tag, err := p.pool.Exec(ctx, `
		UPDATE drivers
		SET status = 'ON_TRIP', current_ride_id = $2::uuid
		WHERE id = $1::uuid
		  AND status = 'AVAILABLE'
		  AND current_ride_id IS NULL`, driverID, rideID)
	if err != nil {
		return 0, err
	}
	if tag.RowsAffected() == 1 {
		return AssignApplied, nil
	}
	var status, ride string
	err = p.pool.QueryRow(ctx, `
		SELECT status, COALESCE(current_ride_id::text, '')
		FROM drivers WHERE id = $1::uuid`, driverID).Scan(&status, &ride)
	if errors.Is(err, pgx.ErrNoRows) {
		return AssignMissing, nil
	}
	if err != nil {
		return 0, err
	}
	if status == StatusOnTrip && ride == rideID {
		return AssignSame, nil
	}
	return AssignOther, nil
}

func (p *Postgres) Release(ctx context.Context, driverID, rideID string) (bool, error) {
	if !validUUID(driverID) || !validUUID(rideID) {
		return false, nil
	}
	tag, err := p.pool.Exec(ctx, `
		UPDATE drivers
		SET status = 'AVAILABLE', current_ride_id = NULL
		WHERE id = $1::uuid AND current_ride_id = $2::uuid`, driverID, rideID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (p *Postgres) List(ctx context.Context) ([]Driver, error) {
	rows, err := p.pool.Query(ctx, `SELECT `+driverColumns+` FROM drivers ORDER BY internal_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Driver
	for rows.Next() {
		d, err := scanDriver(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

type scanner interface {
	Scan(dest ...any) error
}

func getTx(ctx context.Context, q interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}, id string) (Driver, error) {
	row := q.QueryRow(ctx, `SELECT `+driverColumns+` FROM drivers WHERE id = $1::uuid`, id)
	d, err := scanDriver(row)
	if err != nil {
		return Driver{}, err
	}
	return d, nil
}

func insertDriver(ctx context.Context, tx pgx.Tx, id string, in Registration) (Driver, error) {
	var internal int64
	var d Driver
	err := tx.QueryRow(ctx, `
		INSERT INTO drivers (id, name, phone, vehicle_class, plate, status)
		VALUES ($1::uuid, $2, $3, $4, $5, 'PENDING')
		RETURNING internal_id, registered_at`,
		id, in.Name, in.Phone, in.VehicleClass, in.Plate,
	).Scan(&internal, &d.RegisteredAt)
	if isUnique(err, "drivers_plate_key") {
		return Driver{}, ErrPlateTaken
	}
	if err != nil {
		return Driver{}, err
	}
	if internal < 0 || internal > math.MaxUint32 {
		return Driver{}, fmt.Errorf("internal_id %d out of uint32 range", internal)
	}
	d.ID = id
	d.InternalID = uint32(internal)
	d.Name = in.Name
	d.Phone = in.Phone
	d.VehicleClass = in.VehicleClass
	d.Plate = in.Plate
	d.Status = StatusPending
	return d, nil
}

func scanDriver(row scanner) (Driver, error) {
	var d Driver
	var internal int64
	if err := row.Scan(
		&d.ID, &internal, &d.Name, &d.Phone, &d.VehicleClass, &d.Plate,
		&d.Status, &d.CurrentRideID, &d.RegisteredAt,
	); err != nil {
		return Driver{}, err
	}
	if internal < 0 || internal > math.MaxUint32 {
		return Driver{}, fmt.Errorf("internal_id %d out of uint32 range", internal)
	}
	d.InternalID = uint32(internal)
	return d, nil
}

func isUnique(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == constraint
}
