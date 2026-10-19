-- Ranking de rides.matched por tentativa, na ordem em que veio do matching-service.
-- Aplicar numa transação, como ride (o boot do ride-service faz isso quando ride.rankings não existe):
--   psql "postgres://ride:ride@localhost:5432/nearby?sslmode=disable" -v ON_ERROR_STOP=1 -1 -f internal/ride/sql/002_rankings.sql

SET ROLE ride;
SET search_path TO ride;

DO $$
BEGIN
    IF current_user <> 'ride' THEN
        RAISE EXCEPTION 'DDL do schema ride tem que rodar como o usuário ride (atual: %)', current_user;
    END IF;
END
$$;

CREATE TABLE rankings (
    ride_id uuid NOT NULL REFERENCES rides (id),
    attempt integer NOT NULL,
    event_id text NOT NULL,
    engine text NOT NULL,
    candidates jsonb NOT NULL,
    received_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (ride_id, attempt),
    CONSTRAINT rankings_attempt_positive CHECK (attempt >= 1),
    CONSTRAINT rankings_candidates_array CHECK (jsonb_typeof(candidates) = 'array')
);

COMMENT ON TABLE rankings IS 'Último rides.matched aplicado a cada tentativa. Só leitura para o painel; a atribuição não depende daqui.';
COMMENT ON COLUMN rankings.candidates IS 'Array de {driver_id, eta_s, distance_m, location} na ordem do ranking. A posição é o índice + 1.';
