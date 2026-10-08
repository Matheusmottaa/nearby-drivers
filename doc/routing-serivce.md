# routing-service

Calcula o ETA dos candidatos até o ponto de embarque, em cima de um grafo de
ruas carregado na memória. Não conhece corrida, motorista nem passageiro:
recebe coordenadas e devolve distância e duração.

O único chamador é o `matching-service`.

Roda em **2 réplicas** atrás do nginx, na rede interna. É o único serviço que
sobe com mais de uma réplica: stateless, o load balancer reparte a busca.

## O que faz

No boot, cada réplica lê o arquivo do mapa e monta:

- o grafo dirigido em CSR, read-only;
- o grafo reverso, para a busca one-to-many a partir do embarque;
- uma KD-Tree dos nós, para snapping (coordenada → nó mais próximo).

`/readyz` só fica verde quando isso terminou. Não há escrita no grafo depois do
boot.

O ETA de K motoristas até o embarque é **uma** busca de Dijkstra no grafo
reverso, partindo do embarque e parando quando os K pontos foram fixados. Não
são K chamadas.

A busca usa um buffer de trabalho tirado de um pool fixo. Se o pool está cheio,
o serviço responde `503` com `Retry-After` em vez de alocar sem limite.

## Contratos HTTP

API interna. O nginx expõe estes caminhos só para a rede dos serviços.

| Método | Caminho | Quem chama | Resposta |
| --- | --- | --- | --- |
| `POST` | `/routes/eta:batch` | `matching-service` | Duração de várias origens até um destino |
| `GET` | `/nodes/snap` | diagnóstico | Nó do grafo mais próximo da coordenada |
| `GET` | `/healthz`, `/readyz` | nginx | readiness inclui “grafo carregado” |

`POST /routes/eta:batch`:

```json
{
  "destination": { "lat": -23.5613, "lng": -46.6565 },
  "origins": [{ "lat": -23.5601, "lng": -46.6588 }],
  "max_radius_m": 15000
}
```

```json
{
  "estimates": [
    { "origin_index": 0, "duration_s": 142, "distance_m": 820, "reachable": true }
  ],
  "engine": "dijkstra-one-to-many",
  "nodes_visited": 9841,
  "graph_version": "city-1m-v3"
}
```

`destination` é sempre o ponto de embarque, e `origins` são os candidatos. A
resposta identifica a origem por `origin_index`. Mais de 50 origens, ou
coordenada inválida, é `422`. Origem inalcançável volta `reachable: false`
dentro de um `200`.

## Comunicação com outras aplicações

| Origem | Canal | Contrato |
| --- | --- | --- |
| `matching-service` | **REST** `POST /routes/eta:batch` | ETA dos candidatos até o embarque, usado para ordenar o ranking |

Não há chamada de saída e não há Kafka. O matching precisa do número para o
passo seguinte; um tópico entregaria a resposta tarde, para alguém
correlacionar de volta.

Se todas as réplicas caírem, o `matching-service` estima o ETA por Haversine.
O sistema segue atribuindo corrida.

## Cache

Este serviço **não usa Redis** nem cache de ETA. O grafo já é a cópia quente,
e cachear por coordenada explode a chave com a precisão do GPS.

## Banco de dados

Este serviço **não tem banco**. O mapa é um arquivo versionado
(`graph_version` na resposta), igual para todas as réplicas.

## O que o load balancer enxerga

- grafo ainda carregando → fora do pool;
- pool de buscas saturado → `503`, e o `matching-service` tenta de novo, em
  geral noutra réplica;
- processo morto → health check falha, a réplica sai, as outras absorvem.

## Telemetria

| Métrica | Tipo | Por quê |
| --- | --- | --- |
| `http.server.duration` de `eta:batch` por status | histograma | p99 do ETA, que entra direto no tempo de matching |
| `routing.nodes_visited` | histograma | Custo real de cada busca |
| `routing.origins` | histograma | Tamanho dos lotes |
| `routing.unreachable` | contador | Origens fora do grafo ou do raio |
| `routing.pool_in_use` | gauge | Ocupação do pool de buffers |
| `routing.pool_rejected` | contador | `503` por pool cheio |
| `routing.graph_load_ms` | gauge | Tempo de boot até `/readyz` |

Span por chamada, com `engine`, `nodes_visited` e `graph_version` como
atributos.

Log JSON com `service=routing-service`, `trace_id` propagado pelo chamador,
`graph_version`, `engine` e `nodes_visited`.
