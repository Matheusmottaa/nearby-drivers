# Checklist

Controle do que já foi feito. A definição de cada item está em [etapas.md](etapas.md).
Marque `[x]` quando o item estiver no repositório e o teste da etapa passar.

Estado em 11 de outubro de 2026: a base de infraestrutura está pronta, inclusive
nginx, o grafo em `data/maps` (montado em `/data/maps`) e os quatro binários em
`cmd/`. A etapa 1 fecha o boot, o erro HTTP e o envelope dos sete eventos. A
etapa 2 fecha o núcleo puro (`internal/geo`, `internal/kdtree` e a busca de
`internal/graph`). A etapa 3 expõe o ETA e o snap no `routing-service`. A etapa
4 fecha o cadastro e o índice do `driver-service`. A etapa 5 fecha a máquina
de estados, a outbox e a API do `ride-service`. As etapas 6 e 7 ainda não
têm regra de negócio. O DDL de `ride` e `driver` está em `internal/ride/sql` e
`internal/driver/sql` e o serviço dono aplica esse script, como usuário do
schema, ao abrir o Postgres.

## Base do repositório

- [x] Postgres 17 no compose, com schemas `ride` e `driver` e usuários separados (`deploy/postgres/init`)
- [x] Redis só com TTL, sem RDB e sem AOF
- [x] Kafka KRaft e os sete tópicos, com a retenção de cada um (`deploy/kafka/create-topics.sh`)
- [x] `KAFKA_AUTO_CREATE_TOPICS_ENABLE` desligado
- [x] Dockerfiles dos quatro serviços, apontando para `cmd/`
- [x] `Makefile` com `build`, `test` e `docker-build`
- [x] Módulo Go 1.24, com `pgx`, `go-redis` e `franz-go`
- [x] Tabelas SQL dos serviços (`ride` e `driver`)
- [x] nginx
- [x] Arquivo de grafo em `/data/maps`
- [x] Binários em `cmd/`

## Etapa 1 — Contratos de runtime

- [x] `cmd/<serviço>/main.go` lê o ambiente, abre as dependências e desliga no sinal
- [x] `GET /healthz` responde 200 com o processo vivo
- [x] `GET /readyz` responde 503 até o que a doc daquele serviço exige
- [x] DDL versionado do dono do schema, rodando como o usuário daquele schema
- [x] Envelope de evento igual nos sete tópicos: `event_id`, `trace_id`, chave Kafka
- [x] Corpos de `rides.requested`, `rides.matched`, `rides.match-failed`, `rides.assigned`, `rides.released`, `drivers.presence` e `drivers.location`
- [x] Erro HTTP `application/problem+json` com `trace_id`
- [x] `make build` compila os quatro binários
- [x] Cada binário responde `/healthz`
- [x] Teste de mesa serializa e desserializa cada evento sem campo a mais nem a menos

## Etapa 2 — Núcleo do algoritmo

- [x] `internal/geo`: Haversine em metros e rejeição de coordenada inválida
- [x] Fallback: distância em linha reta dividida por `HAVERSINE_SPEED_MPS`, com `engine: haversine-fallback`
- [x] `internal/kdtree`: vizinho mais próximo, imutável depois do boot (nós do grafo)
- [x] `internal/kdtree`: snapshot atômico mais delta para motoristas disponíveis
- [x] Leitura aplica o delta, filtra classe e raio, ordena por `straight_line_m` e corta em K
- [x] `internal/graph`: loader do arquivo, CSR, grafo reverso e KD-Tree dos nós
- [x] Dijkstra one-to-many no reverso, peso `duration_s`, parada em K origens ou `max_radius_m`
- [x] Pool fixo de buffers; pool esgotado é saturação; o buffer volta também se o contexto cancela
- [x] Teste: Haversine de um par conhecido e coordenada inválida
- [x] Teste: K dentro do raio, lista vazia, empate, snapshot trocado no meio da leitura, delta que corrige o snapshot
- [x] Teste: grafo mínimo (corredor, beco, dois caminhos); beco `reachable: false`; tempo ganha de metros
- [x] Teste: pool de tamanho 1 satura a segunda busca e devolve o buffer

## Etapa 3 — routing-service

- [x] Boot lê o grafo, monta CSR, reverso e KD-Tree; `/readyz` fica 503 até o load
- [x] Arquivo do grafo fora da imagem, volume em `/data/maps`
- [x] `graph_version` é a primeira linha do arquivo
- [x] `POST /routes/eta:batch` com `estimates`, `engine`, `nodes_visited`, `graph_version`
- [x] `GET /nodes/snap`
- [x] Mais de 50 origens ou coordenada inválida: `422`
- [x] Origem inalcançável ou fora do raio: `200` com `reachable: false`
- [x] Pool cheio: `503` e `Retry-After`
- [x] Grafo ainda carregando: `503`
- [x] `engine` desta rota é sempre `dijkstra-one-to-many`
- [x] Uma instância nesta etapa
- [x] Teste `httptest` com o grafo mínimo: alcançável e beco, `422`, `503` de pool, `/readyz` antes e depois do load
- [x] Nenhuma chamada de saída

## Etapa 4 — driver-service

- [x] Tabela `drivers` no schema `driver`
- [x] `internal_id` `uint32` denso, estável, índice do array de slots
- [x] Slot em memória: lat, lng, `seq`, heartbeat, disponibilidade, reserva. Posição sem coluna
- [x] Boot: Postgres, replay de 5 minutos de `drivers.location`, primeiro snapshot, então `/readyz` 200
- [x] Redis `driver:{id}:last`, TTL 60 s. O `nearby` não lê essa chave
- [x] `POST /drivers` com `Idempotency-Key`; a mesma chave devolve o cadastro original
- [x] `GET /drivers/{id}` sem trilha de GPS
- [x] `PATCH /drivers/{id}/status`; transição ilegal: `409`
- [x] `PUT /drivers/{id}/location` com `seq`; `seq` velho devolve `204` e não altera o slot
- [x] `GET /drivers/nearby` ordenado por `straight_line_m`; lista vazia é `200`
- [x] `k > 100`, `radius_m > 50000` ou coordenada inválida: `422`. Índice ausente: `503`
- [x] `nearby` exige `AVAILABLE`, heartbeat nos últimos 30 s e sem reserva
- [x] `rides.assigned` e `rides.released` atualizam o bit e o `UPDATE` condicional
- [x] `released` só libera se o `ride_id` for o da reserva atual
- [x] Varredor publica `lost` uma vez por reserva sem heartbeat
- [x] `204` sai depois da escrita local; o publish de `drivers.location` é assíncrono
- [x] Uma instância
- [x] Teste do slot: `seq` velho, heartbeat de 30 s, bit de reserva, `released` de outra corrida
- [x] Teste HTTP: cadastro, idempotência, `nearby` vazio, `nearby` ordenado, `503` antes do snapshot
- [x] Teste de integração: replay no boot reconstitui o slot

## Etapa 5 — ride-service

- [x] Tabelas `rides`, `outbox` e `idempotency_keys` no schema `ride`
- [x] Índice único parcial em `assigned_driver_id` onde `status = 'DRIVER_ASSIGNED'`
- [x] Índice `(status, updated_at)` para status ainda não terminal
- [x] Corrida e outbox na mesma transação
- [x] Publisher com `FOR UPDATE SKIP LOCKED`; queda no meio reenvia o mesmo `event_id`
- [x] Publish de `rides.requested` passa a corrida de `REQUESTED` para `MATCHING`
- [x] Redis `idem:{key}` (24 h) e `driver:{id}:res` (30 s, `SETNX`)
- [x] Redis fora do ar: `idempotency_keys` e o índice único seguem valendo
- [x] Consumer group `ride-workers`, um consumidor
- [x] `POST /rides` responde `202` com `Location` e `poll_after_ms`
- [x] Mesma `Idempotency-Key` e mesmo corpo: `200`. Corpo diferente: `409`
- [x] `GET /rides/{id}`, `wait=3s`, `ETag` / `If-None-Match` → `304`
- [x] `GET /rides/{id}/events` em SSE
- [x] `POST /rides/{id}/cancel`, inclusive se já estava cancelada
- [x] `rides.matched` só no `attempt` atual; `attempt` menor recebe ack e segue
- [x] Reserva percorre o ranking; conflito passa ao próximo; ranking esgotado vai para `NO_DRIVER_AVAILABLE`
- [x] `rides.match-failed` no `attempt` atual vai para `NO_DRIVER_AVAILABLE`
- [x] `lost` publica `rides.released` e, se ainda há tentativa, um novo `rides.requested`
- [x] Segundo `lost` da mesma corrida não transiciona de novo
- [x] Cancelar com motorista atribuído publica `rides.released`
- [x] `version` sobe em toda transição
- [x] Uma instância. Nenhuma chamada REST
- [x] Teste de tabela da máquina: cada seta, `attempt` estourado, ranking esgotado, `matched` repetido, attempt velho, cancelamento repetido
- [x] Teste de integração: outbox na mesma transação, reenvio do `event_id`, duas atribuições do mesmo motorista

## Etapa 6 — matching-service

- [ ] Consumer group `matchers`, um consumidor. Ack só depois do publish
- [ ] Redis `evt:{event_id}`, TTL 1 h. Redis fora do ar não para o serviço
- [ ] Clientes de `nearby` e `eta:batch`: timeout 800 ms, 2 retries com jitter, circuit breaker
- [ ] Breaker aberto cai no Haversine e não vira `match-failed`
- [ ] Ignora `event_id` já visto
- [ ] Pede até `MATCH_K` candidatos, teto 50, e remove `exclude_driver_ids` antes do ETA
- [ ] Lista vazia depois da exclusão: `match-failed` com `no_candidate`
- [ ] Uma chamada `eta:batch`; descarta `reachable: false`; ordena por `NearestETA`
- [ ] Desempate: `distance_m` menor, depois `driver_id` crescente
- [ ] Ninguém alcançável: `match-failed` com `none_reachable`
- [ ] Routing fora: ranking com `engine: haversine-fallback`
- [ ] Ranking publicado inteiro, com o `attempt` intacto
- [ ] `/healthz` e `/readyz`; ready exige broker. Sem rota de produto
- [ ] Uma instância, fora do nginx público
- [ ] Teste com fakes: ranking por ETA, exclusão antes do ETA, fallback Haversine, `no_candidate`, `none_reachable`, `event_id` único, ack só se o publish funciona
- [ ] Teste de integração: `POST /rides` até `DRIVER_ASSIGNED`, com o `trace_id` no evento e no log

## Etapa 7 — Nginx, réplicas do routing e o compose fechado

- [ ] nginx como único host do passageiro e do app do motorista
- [ ] `ride-service`: 1 réplica, rotas `/rides`
- [ ] `driver-service`: 1 réplica, rotas `/drivers`
- [ ] `routing-service`: 2 réplicas, só na rede interna, `/routes` e `/nodes`
- [ ] `matching-service`: 1 réplica, fora do nginx público
- [ ] Réplica de routing com `/readyz` 503 sai do pool
- [ ] `503` de pool de Dijkstra faz o matching tentar de novo na outra réplica
- [ ] Compose sobe os quatro serviços, o nginx, o volume do grafo, Postgres, Redis e Kafka
- [ ] `make docker-build` gera as quatro imagens
- [ ] As duas réplicas de routing respondem a mesma `graph_version`
- [ ] Script: cadastra motorista, `AVAILABLE`, localização
- [ ] Script: `POST /rides` recebe `202` e `REQUESTED`
- [ ] Script: `GET` com `wait=3s`, ou o SSE, chega em `DRIVER_ASSIGNED` com `eta_s`, `distance_m` e `eta_engine`
- [ ] Script: a mesma `Idempotency-Key` e o mesmo corpo devolvem a mesma corrida
- [ ] Script: parar o heartbeat gera `lost` e rematch, ou `NO_DRIVER_AVAILABLE` se o `attempt` estourou
- [ ] Script: cancelar a corrida atribuída publica `rides.released` e o motorista volta ao `nearby`
- [ ] Script: derrubar as réplicas de routing ainda atribui corrida, com `engine: haversine-fallback`
