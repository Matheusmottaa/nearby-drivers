# nearby-drivers

Sistema de pedido de corrida que encontra o motorista disponível mais próximo do
ponto de embarque e devolve ao passageiro quem vem e em quanto tempo.

Na v2 a corrida termina em `DRIVER_ASSIGNED`: não há destino, rota nem viagem.

## Arquitetura

![Diagrama de arquitetura](doc/asset/architecture_diagram.png)

O passageiro entra pelo nginx, que balanceia as réplicas dos serviços HTTP e
tira do pool quem não responde ao `/readyz`. O `POST /rides` grava a corrida e
responde `202` sem esperar motorista; o matching acontece de forma assíncrona
via Kafka.

1. `ride-service` grava a corrida em `REQUESTED` com o evento na outbox e
   publica `rides.requested`.
2. `matching-service` consome o pedido, busca candidatos no `driver-service`
   (`GET /drivers/nearby`) e pede o ETA de todos ao `routing-service`
   (`POST /routes/eta:batch`) numa única chamada.
3. `matching-service` publica `rides.matched` com o ranking inteiro, ordenado
   por ETA.
4. `ride-service` reserva o primeiro motorista livre do ranking, passa a
   corrida para `DRIVER_ASSIGNED` e publica `rides.assigned`.
5. `driver-service` marca o motorista como `ON_TRIP` e publica
   `drivers.presence` (`lost`) se ele parar de mandar heartbeat, disparando um
   rematch.

## Repositórios

| Serviço | Responsabilidade | Réplicas | Documentação | Repositório |
| --- | --- | --- | --- | --- |
| `ride-service` | Dono do estado da corrida: cria, atribui e cancela | 2 | [doc/ride-service.md](doc/ride-service.md) | [Matheusmottaa/ride-service](https://github.com/Matheusmottaa/ride-service) |
| `driver-service` | Cadastro do motorista e índice de posições em memória (KD-Tree) | 2 | [doc/driver-service.md](doc/driver-service.md) | [Matheusmottaa/driver-service](https://github.com/Matheusmottaa/driver-service) |
| `matching-service` | Monta o ranking de motoristas pelo ETA até o embarque | 2 | [doc/matching-service.md](doc/matching-service.md) | [Matheusmottaa/matching-service](https://github.com/Matheusmottaa/matching-service) |
| `routing-service` | ETA one-to-many sobre o grafo de ruas em memória | 3 | [doc/routing-serivce.md](doc/routing-serivce.md) | [Matheusmottaa/routing-service](https://github.com/Matheusmottaa/routing-service) |

## Infraestrutura

| Componente | Uso |
| --- | --- |
| nginx | Load balancer e health check das réplicas |
| Kafka | `rides.*` (7 dias), `drivers.location` (fan-out, 5 minutos), `drivers.presence` (1 hora) |
| PostgreSQL | Schema `ride` (corridas, outbox, idempotência) e schema `driver` (cadastro) |
| Redis | Idempotency key, dedup de `event_id`, reserva rápida e última posição. Só TTL, sem persistência |
