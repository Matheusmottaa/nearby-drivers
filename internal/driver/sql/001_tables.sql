-- Tabela do schema driver.
-- Dono: usuário driver. O init em deploy/postgres/init só cria schema e role.
-- Só o cadastro e a reserva. Lat/lng, seq e heartbeat ficam no slot em memória.
-- Aplicar numa transação, como driver (o boot do driver-service faz isso na etapa 1):
--   psql "postgres://driver:driver@localhost:5432/nearby?sslmode=disable" -v ON_ERROR_STOP=1 -1 -f internal/driver/sql/001_tables.sql

SET ROLE driver;
SET search_path TO driver;

DO $$
BEGIN
    IF current_user <> 'driver' THEN
        RAISE EXCEPTION 'DDL do schema driver tem que rodar como o usuário driver (atual: %)', current_user;
    END IF;
END
$$;

CREATE TABLE drivers (
    id uuid PRIMARY KEY,
    -- uint32 denso: índice do array de slots. bigint porque 4294967295 não cabe em integer.
    -- Nasce neste INSERT e não muda. O primeiro valor é 0.
    internal_id bigint NOT NULL GENERATED ALWAYS AS IDENTITY (
        SEQUENCE NAME drivers_internal_id_seq
        START WITH 0
        MINVALUE 0
        MAXVALUE 4294967295
        INCREMENT BY 1
        NO CYCLE
    ),
    name text NOT NULL,
    phone text NOT NULL,
    vehicle_class text NOT NULL,
    plate text NOT NULL,
    status text NOT NULL,
    current_ride_id uuid,
    registered_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT drivers_internal_id_uint32 CHECK (internal_id BETWEEN 0 AND 4294967295),
    CONSTRAINT drivers_internal_id_key UNIQUE (internal_id),
    CONSTRAINT drivers_plate_key UNIQUE (plate),
    CONSTRAINT drivers_name_present CHECK (name <> ''),
    CONSTRAINT drivers_phone_present CHECK (phone <> ''),
    CONSTRAINT drivers_vehicle_class_present CHECK (vehicle_class <> ''),
    CONSTRAINT drivers_plate_present CHECK (plate <> ''),
    CONSTRAINT drivers_status_known CHECK (
        status IN ('PENDING', 'OFFLINE', 'AVAILABLE', 'ON_TRIP', 'SUSPENDED')
    ),
    -- NULL fora de reserva. released só libera se o ride_id do evento for o atual.
    CONSTRAINT drivers_reservation_matches_status CHECK (
        (status = 'ON_TRIP' AND current_ride_id IS NOT NULL)
        OR (status <> 'ON_TRIP' AND current_ride_id IS NULL)
    )
);

COMMENT ON TABLE drivers IS 'Cadastro do motorista e estado de reserva. Sem coluna de posição.';
COMMENT ON COLUMN drivers.internal_id IS 'uint32 denso e estável entre reinícios. Índice do array de slots em memória.';
COMMENT ON COLUMN drivers.current_ride_id IS 'NULL fora de ON_TRIP. Um release de outra corrida não libera esta reserva.';
COMMENT ON COLUMN drivers.status IS 'PENDING, OFFLINE, AVAILABLE, ON_TRIP ou SUSPENDED.';
