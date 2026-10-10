-- Tabelas do schema ride.
-- Dono: usuário ride. O init em deploy/postgres/init só cria schema e role.
-- Aplicar numa transação, como ride (o boot do ride-service faz isso na etapa 1):
--   psql "postgres://ride:ride@localhost:5432/nearby?sslmode=disable" -v ON_ERROR_STOP=1 -1 -f internal/ride/sql/001_tables.sql

SET ROLE ride;
SET search_path TO ride;

DO $$
BEGIN
    IF current_user <> 'ride' THEN
        RAISE EXCEPTION 'DDL do schema ride tem que rodar como o usuário ride (atual: %)', current_user;
    END IF;
END
$$;

CREATE TABLE rides (
    id uuid PRIMARY KEY,
    passenger_id uuid NOT NULL,
    status text NOT NULL,
    pickup_lat double precision NOT NULL,
    pickup_lng double precision NOT NULL,
    vehicle_class text NOT NULL,
    assigned_driver_id uuid,
    assigned_eta_s integer,
    assigned_distance_m integer,
    eta_engine text,
    excluded_driver_ids uuid[] NOT NULL DEFAULT '{}',
    attempt integer NOT NULL DEFAULT 1,
    cancel_reason text,
    version integer NOT NULL DEFAULT 1,
    requested_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT rides_status_known CHECK (
        status IN (
            'REQUESTED',
            'MATCHING',
            'DRIVER_ASSIGNED',
            'CANCELLED',
            'NO_DRIVER_AVAILABLE'
        )
    ),
    CONSTRAINT rides_pickup_lat_range CHECK (pickup_lat BETWEEN -90 AND 90),
    CONSTRAINT rides_pickup_lng_range CHECK (pickup_lng BETWEEN -180 AND 180),
    CONSTRAINT rides_vehicle_class_present CHECK (vehicle_class <> ''),
    CONSTRAINT rides_attempt_positive CHECK (attempt >= 1),
    CONSTRAINT rides_version_positive CHECK (version >= 1),
    CONSTRAINT rides_updated_after_request CHECK (updated_at >= requested_at),
    CONSTRAINT rides_eta_engine_known CHECK (
        eta_engine IS NULL
        OR eta_engine IN ('dijkstra-one-to-many', 'haversine-fallback')
    ),
    CONSTRAINT rides_assigned_metrics_nonnegative CHECK (
        (assigned_eta_s IS NULL OR assigned_eta_s >= 0)
        AND (assigned_distance_m IS NULL OR assigned_distance_m >= 0)
    ),
    -- DRIVER_ASSIGNED só existe com o motorista e o ETA prometido pelo ranking.
    CONSTRAINT rides_assignment_complete CHECK (
        status <> 'DRIVER_ASSIGNED'
        OR (
            assigned_driver_id IS NOT NULL
            AND assigned_eta_s IS NOT NULL
            AND assigned_distance_m IS NOT NULL
            AND eta_engine IS NOT NULL
        )
    )
);

-- Garantia final da reserva. O SETNX driver:{id}:res no Redis é só o atalho.
CREATE UNIQUE INDEX rides_assigned_driver_id_active_uidx
    ON rides (assigned_driver_id)
    WHERE status = 'DRIVER_ASSIGNED';

-- Fora do índice: DRIVER_ASSIGNED (lost e cancelamento chegam por evento),
-- CANCELLED e NO_DRIVER_AVAILABLE. Sobra corrida presa em REQUESTED ou MATCHING.
CREATE INDEX rides_status_updated_at_open_idx
    ON rides (status, updated_at)
    WHERE status NOT IN ('DRIVER_ASSIGNED', 'CANCELLED', 'NO_DRIVER_AVAILABLE');

CREATE TABLE outbox (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    event_id uuid NOT NULL UNIQUE,
    aggregate_id uuid NOT NULL REFERENCES rides (id),
    subject text NOT NULL,
    payload jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    published_at timestamptz,
    CONSTRAINT outbox_subject_known CHECK (
        subject IN ('rides.requested', 'rides.assigned', 'rides.released')
    ),
    CONSTRAINT outbox_payload_object CHECK (jsonb_typeof(payload) = 'object'),
    CONSTRAINT outbox_published_after_create CHECK (
        published_at IS NULL OR published_at >= created_at
    )
);

-- Publisher lê published_at IS NULL ORDER BY id FOR UPDATE SKIP LOCKED.
-- Cair no meio reenvia o mesmo event_id.
CREATE INDEX outbox_unpublished_idx
    ON outbox (id)
    WHERE published_at IS NULL;

CREATE TABLE idempotency_keys (
    key text PRIMARY KEY,
    body_hash bytea NOT NULL,
    ride_id uuid NOT NULL REFERENCES rides (id),
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT idempotency_keys_key_present CHECK (key <> ''),
    CONSTRAINT idempotency_keys_body_hash_present CHECK (octet_length(body_hash) > 0)
);

COMMENT ON TABLE rides IS 'Agregado da corrida. A posição do motorista não fica aqui.';
COMMENT ON COLUMN rides.assigned_driver_id IS 'NULL até atribuir. Único enquanto status = DRIVER_ASSIGNED.';
COMMENT ON COLUMN rides.assigned_eta_s IS 'ETA prometido, copiado do ranking.';
COMMENT ON COLUMN rides.assigned_distance_m IS 'Distância prometida, copiada do ranking.';
COMMENT ON COLUMN rides.eta_engine IS 'dijkstra-one-to-many ou haversine-fallback.';
COMMENT ON COLUMN rides.excluded_driver_ids IS 'Motoristas que sumiram nesta corrida.';
COMMENT ON COLUMN rides.version IS 'Versão otimista. Sobe em toda transição.';
COMMENT ON TABLE outbox IS 'Mesma transação da corrida. published_at fica NULL até o publisher confirmar.';
COMMENT ON COLUMN outbox.event_id IS 'Estável no reenvio. A entrega repetida não gera outro id.';
COMMENT ON TABLE idempotency_keys IS 'Idempotency-Key do POST /rides. O hash separa o mesmo corpo de um corpo diferente.';
COMMENT ON INDEX rides_assigned_driver_id_active_uidx IS 'Um motorista só pode estar em uma corrida DRIVER_ASSIGNED.';
COMMENT ON INDEX rides_status_updated_at_open_idx IS 'Corridas não terminais (REQUESTED, MATCHING), para achar pedido preso e retentar.';
