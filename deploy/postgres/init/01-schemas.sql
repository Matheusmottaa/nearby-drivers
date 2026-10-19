-- Schemas separados. Cada serviço só enxerga o próprio.
-- Senhas locais: ride/ride e driver/driver. O superusuário nearby/nearby fica para administração.

REVOKE CREATE ON SCHEMA public FROM PUBLIC;

CREATE USER ride WITH PASSWORD 'ride';
CREATE SCHEMA ride AUTHORIZATION ride;
ALTER ROLE ride SET search_path TO ride;
REVOKE ALL ON SCHEMA ride FROM PUBLIC;

CREATE USER driver WITH PASSWORD 'driver';
CREATE SCHEMA driver AUTHORIZATION driver;
ALTER ROLE driver SET search_path TO driver;
REVOKE ALL ON SCHEMA driver FROM PUBLIC;

-- Leitura do painel de corridas no Grafana. Só SELECT nas tabelas que o ride criar no schema ride.
CREATE USER grafana WITH PASSWORD 'grafana';
GRANT USAGE ON SCHEMA ride TO grafana;
ALTER DEFAULT PRIVILEGES FOR ROLE ride IN SCHEMA ride GRANT SELECT ON TABLES TO grafana;
ALTER ROLE grafana SET default_transaction_read_only = on;
