# matching-service

Consome um pedido de corrida, monta um ranking de motoristas pelo ETA até o
embarque e publica o resultado. Não grava corrida, não reserva motorista e não
calcula rota. É uma função: pedido mais o estado do mundo naquele instante →
lista ordenada.

Roda no consumer group `matchers`. No cluster começa com 1 pod e o autoscaler
sobe até 3, uma cópia por partição de `rides.requested`, quando a CPU passa de
60%. Não fica atrás do nginx público: ninguém chama este serviço por HTTP de
produto. Só `/healthz` e `/readyz`.

## O que faz

Para cada mensagem `rides.requested`:

1. Ignora `event_id` já visto.
2. Pede até K candidatos ao `driver-service` (`GET /drivers/nearby`), com o
   ponto de embarque, o raio e a classe de veículo do pedido.
3. Manda as coordenadas desses candidatos numa única chamada ao
   `routing-service` (`POST /routes/eta:batch`), com o embarque como destino.
   Uma busca a partir do embarque, não K rotas separadas.
4. Descarta quem voltou `reachable: false`, ordena por ETA (política
   `NearestETA`) e publica `rides.matched` com o **ranking inteiro**.
5. Se não houver candidato alcançável, publica `rides.match-failed`.

O ranking completo existe porque a reserva pode falhar no `ride-service` (outro
pedido levou o primeiro motorista). O segundo e o terceiro já vêm na mensagem.

Se o `routing-service` estiver fora ou o circuit breaker abrir, o ETA cai para
distância em linha reta dividida por uma velocidade média
(`engine: haversine-fallback`). Falha de routing não pode virar
`NO_DRIVER_AVAILABLE`.

O matching olha o embarque.

## Contratos

Não há endpoint de negócio. O contrato é o evento.

### Consome `rides.requested`

Consumer group `matchers` (competing consumers).

```json
{
  "event_id": "018f3a2b-aaaa-7abc-8def-0123456789ab",
  "trace_id": "req-abc123",
  "ride_id": "018f3a2b-2222-7abc-8def-0123456789ab",
  "attempt": 1,
  "pickup": { "lat": -23.5613, "lng": -46.6565 },
  "vehicle_class": "standard",
  "exclude_driver_ids": []
}
```

`exclude_driver_ids` vem preenchido no rematch: o motorista que sumiu não volta
no ranking da mesma corrida. `attempt` é devolvido no resultado sem alteração.

### Produz `rides.matched`

```json
{
  "event_id": "018f3a2b-bbbb-7abc-8def-0123456789ab",
  "trace_id": "req-abc123",
  "ride_id": "018f3a2b-2222-7abc-8def-0123456789ab",
  "attempt": 1,
  "engine": "dijkstra-one-to-many",
  "ranking": [
    {
      "driver_id": "018f9c00-3333-7abc-8def-0123456789ab",
      "eta_s": 142,
      "distance_m": 820,
      "location": { "lat": -23.5601, "lng": -46.6588 }
    }
  ]
}
```

`rides.match-failed` leva `ride_id`, `attempt`, `trace_id`, `event_id` e o motivo
(`no_candidate` ou `none_reachable`).

O ack no Kafka só sai depois do publish do resultado.

## Comunicação com outras aplicações

| Destino | Canal | Para quê |
| --- | --- | --- |
| `ride-service` | **Kafka** `rides.requested` (consome) e `rides.matched` / `rides.match-failed` (produz) | Entrada e saída do trabalho. Não há HTTP entre os dois |
| `driver-service` | **REST** `GET /drivers/nearby` | Lista de candidatos perto do embarque |
| `routing-service` | **REST** `POST /routes/eta:batch` | ETA de cada candidato até o embarque, para ordenar |

Os dois REST têm timeout, retry com jitter e circuit breaker. O retry é seguro
porque `nearby` e `eta:batch` são leituras.

## Kafka — por que esta aplicação usa

- O `POST /rides` já respondeu. Este serviço pode levar o tempo que a busca
  levar.
- O consumer group `matchers` tem um consumidor: cada corrida chega nele.
- Se o processo morrer durante o nearby ou o ETA, a mensagem volta.
- At-least-once é o contrato do broker; a duplicata é tratada abaixo.

Chave `ride_id` nos tópicos de entrada e saída. Retenção de 7 dias.

## Cache — por que esta aplicação usa

Só deduplicação de evento.

| Chave | TTL | Uso |
| --- | --- | --- |
| `evt:{event_id}` | 1 h | `SET` só na primeira vez. A segunda entrega recebe ack e não publica outro ranking |

Se o Redis cair, o serviço continua. A segunda linha de defesa está no
`ride-service`, que só atribui corrida em `MATCHING`.

## Banco de dados

Este serviço **não tem banco**. O ranking é derivado de posições e de um grafo
que já têm dono.

## Telemetria

| Métrica | Tipo | Por quê |
| --- | --- | --- |
| `matching.duration_ms` por etapa (`nearby`, `eta`, `rank`) | histograma | Separa índice lento de routing lento |
| `matching.candidates` | histograma | Quantos candidatos o `nearby` trouxe |
| `matching.unreachable` | contador | Candidatos descartados com `reachable: false` |
| `matching.fallback` | contador | Rankings feitos com Haversine. Sobe quando o routing cai |
| `matching.failed` por motivo | contador | `no_candidate` e `none_reachable` |
| `matching.duplicate_event` | contador | Entregas repetidas barradas no Redis |
| `kafka.consumer.lag` (`rides.requested`) | gauge | Pedidos esperando matching |

`GET /metrics` expõe os mesmos contadores no texto do Prometheus, sem biblioteca
cliente. O log mantém o nome da tabela. No scrape, `matching.fallback` aparece
como `matching_fallback_total` e como
`matching_rankings_total{engine="haversine-fallback"}`.
`matching_routing_breaker_open` fica em 1 enquanto o circuit breaker do routing
recusa chamada.

Spans: consumo de `rides.requested`, chamada ao `nearby`, chamada ao
`eta:batch` e publish de `rides.matched`, todos com o `trace_id` do pedido.

Log JSON com `service=matching-service`, `trace_id`, `event_id` e `ride_id`.
