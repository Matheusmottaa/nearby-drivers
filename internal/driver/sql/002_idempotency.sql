-- Idempotency-Key do POST /drivers. A mesma chave devolve o cadastro original,
-- sem um segundo internal_id. Não guarda posição.
-- Aplicar como usuário driver, depois de 001_tables.sql.

SET ROLE driver;
SET search_path TO driver;

DO $$
BEGIN
    IF current_user <> 'driver' THEN
        RAISE EXCEPTION 'DDL do schema driver tem que rodar como o usuário driver (atual: %)', current_user;
    END IF;
END
$$;

CREATE TABLE idempotency_keys (
    key text PRIMARY KEY,
    driver_id uuid NOT NULL REFERENCES drivers (id),
    CONSTRAINT idempotency_keys_key_present CHECK (key <> '')
);

COMMENT ON TABLE idempotency_keys IS 'Idempotency-Key do POST /drivers. A mesma chave devolve o motorista original.';
