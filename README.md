# Nexus Event Gateway

[![CI](https://github.com/deboracastrodev/distribuid-system-simulator/actions/workflows/ci.yml/badge.svg)](https://github.com/deboracastrodev/distribuid-system-simulator/actions/workflows/ci.yml)

Simulador de sistema distribuido com garantias **Exactly-Once** para processamento de eventos de pedidos.

## Arquitetura

```mermaid
graph LR
    A[Agent Python<br/>LangGraph] -->|Kafka| B[Server Go<br/>Event Processor]
    B -.->|cache| C[(Redis<br/>Lua Script)]
    B --> D[(Postgres<br/>Source of Truth)]
    B --> E[DLQ<br/>Dead Letter Queue]
    B -->|Webhooks do outbox| W[Webhook Sink<br/>receptor de teste]
    F[Consul] -.->|Service Discovery<br/>Circuit Breaker| B
    G[Jaeger] -.->|Traces OTLP| A & B
    P[Prometheus] -.->|scrape /metrics| B
    H[Grafana] -.->|Dashboards| G & P
```

### Fluxo de um Pedido

```mermaid
sequenceDiagram
    participant Agent as Agent (Python)
    participant Kafka
    participant Server as Server (Go)
    participant Redis
    participant Postgres

    Agent->>Agent: generate_plan (valida items/amount)
    loop Para cada passo do plano (seq 1..5)
        Agent->>Kafka: EventEnvelope {plan_id, seq_id, event_type}, publicado assim que o node o gera
    end
    opt estoque indisponivel ou pagamento recusado (simulados)
        Agent->>Kafka: ABORT_PLAN {abort_code, aborted_at_seq}
    end

    Kafka->>Server: Poll records (manual commit)
    Server->>Redis: Lookup(plan_id) — cache opcional
    alt cache prova DUPLICATE ou ABORTED
        Redis-->>Server: pula o evento
    else miss, cache atrasado ou Redis fora
        Server->>Postgres: BEGIN + advisory lock(order_id)
        Postgres-->>Server: estado do pedido (last_seq, status, plan_id)
        alt APPLY (seq == last_seq + 1)
            Server->>Postgres: order + outbox + drena pending_events consecutivos
        else BUFFER (gap)
            Server->>Postgres: INSERT pending_events
        else DUPLICATE / DISCARD (abortado)
            Note over Server,Postgres: nada a gravar
        end
        Server->>Postgres: COMMIT
        Server->>Redis: Advance(plan_id, last_seq) — so apos o commit
    end

    alt erro transitorio (PG fora, timeout)
        Server->>Server: retry no lugar com backoff (nao avanca)
    else erro permanente (evento invalido, dado rejeitado)
        Server->>Kafka: DLQ (espera ack do broker)
    end
    Server->>Kafka: CommitRecords (so registros resolvidos)
```

### Componentes

| Componente | Tech | Responsabilidade |
|---|---|---|
| **Agent** | Python 3.12, LangGraph, Pydantic | Gera planos de eventos com sequenciamento |
| **Server** | Go 1.22, franz-go | Consome Kafka, valida sequencia, persiste |
| **Redis** | Redis 7.2, Lua Script | Cache monotonico de progresso por plano (opcional) |
| **Postgres** | PostgreSQL 16 | Source of truth: sequencia, buffer de reordenacao, outbox |
| **Consul** | Consul 1.22 | Service discovery + config dinamica CB |
| **Kafka** | Apache Kafka 3.7 | Transporte com manual commit |
| **Jaeger** | OTLP gRPC | Distributed tracing |
| **Grafana** | Dashboards | Metricas e visualizacao de traces |
| **Prometheus** | Prometheus 3.13 | Scrape de `/metrics` do server a cada 5s |
| **Webhook Sink** | Python (stdlib) | Receptor de webhooks para testes: registra entregas, verifica ordem, duplicatas e `Idempotency-Key`, e injeta falhas |

## Garantias de Processamento

O objetivo e *effectively-once*: cada evento altera o pedido e gera notificacao no outbox exatamente uma vez, em ordem, mesmo com reentrega, reordenacao e falhas.

1. **Postgres decide tudo numa transacao** (`internal/db`): um advisory lock por `order_id` serializa quem escreve no pedido, inclusive antes de a linha existir. Na mesma transacao o servico le o estado, decide (`internal/sequencing`), grava `orders` + `outbox` e drena os eventos consecutivos de `pending_events`. Se a transacao falha, nada muda em lugar nenhum.
2. **Kafka com commit so do que foi resolvido**: um erro transitorio (Postgres fora, timeout) e retentado no lugar com backoff exponencial, sem pular o registro nem commitar depois dele. So vai para a DLQ o que nunca vai funcionar (JSON invalido, envelope invalido, dado rejeitado pelo banco, plano divergente), e o offset so e commitado depois do ack do broker.
3. **Redis e cache, nao fonte de verdade**: o cache so e escrito depois do commit no Postgres, e o `advance_seq.lua` nunca faz o contador voltar. Por isso o cache pode ficar atras do banco, mas nunca a frente, e so e usado para *pular* duplicatas e eventos de planos abortados. Se o Redis perde os dados ou sai do ar, o servico continua (o cache e ignorado por 5s apos cada falha).

| Situacao | Resultado |
|---|---|
| Evento repetido | `DUPLICATE`: ignorado, sem nova notificacao |
| Gap na sequencia | `BUFFER` em `pending_events`, aplicado quando o antecessor chega |
| Antecessor nunca chega | depois de 1h o evento vai para a DLQ (`BUFFER_TIMEOUT`) |
| `ABORT_PLAN` com pedido em andamento | pedido `aborted`, buffer descartado, 1 notificacao |
| `ABORT_PLAN` antes de qualquer evento | tombstone `aborted`; eventos que chegarem depois sao descartados |
| `ABORT_PLAN` com pedido completo | ignorado (estado terminal) |
| Evento de outro plano para o mesmo pedido | DLQ (`PLAN_MISMATCH`) |
| JSON malformado ou envelope invalido | DLQ (`PARSE_ERROR` / `INVALID_EVENT`), a particao segue |
| Redis fora do ar ou sem dados | processamento segue pelo Postgres |
| Postgres fora do ar | retry no lugar; nada e commitado nem perdido |
| Receptor de webhook com erro (5xx, 408, 429, timeout) | retry agendado no banco com backoff exponencial; a notificacao seguinte do mesmo pedido espera, os outros pedidos seguem |
| Receptor rejeita a notificacao (outro 4xx) | dead-letter na hora, sem retry; as notificacoes seguintes do pedido continuam |
| Webhook falha 10 vezes seguidas | dead-letter (`outbox.dead_at`) |
| Dispatcher morre durante a entrega | o lease expira e outra instancia reenvia; o receptor deduplica pela `Idempotency-Key` |

### Entrega de notificacoes (outbox → webhook)

A entrega e *at-least-once*: se o dispatcher morrer entre a resposta HTTP e o registro no banco, a notificacao e reenviada. Por isso toda requisicao leva a `Idempotency-Key` (o ID da entrada do outbox) para o receptor deduplicar. Tambem sao enviados os headers `X-Aggregate-ID`, `X-Event-Type`, `X-Outbox-Position` e `X-Delivery-Attempt`.

- **Ordem por pedido:** so a notificacao pendente mais antiga de cada pedido pode ser entregue. A seguinte espera a anterior ser entregue ou ir para dead-letter.
- **Sem bloqueio entre pedidos:** um pedido com falha nao atrasa os outros; ate `WEBHOOK_WORKERS` pedidos sao entregues em paralelo.
- **Varias instancias:** `FOR UPDATE SKIP LOCKED` mais um lease de 1 minuto garantem que duas instancias nao entreguem a mesma notificacao ao mesmo tempo.
- **Circuit breaker aberto:** nada e reivindicado nem contado como tentativa.

Detalhes e trade-offs no ADR-005 (`docs/blueprint-arquitetura.md`).

## Metricas

O server expoe `/metrics` na porta 8080. O Prometheus faz scrape a cada 5s, e o dashboard **Nexus Event Gateway - Metricas** (pasta Nexus no Grafana) mostra tudo abaixo.

| Metrica | Tipo | O que mede |
|---|---|---|
| `nexus_events_processed_total{kind, outcome}` | counter | Eventos resolvidos pelo consumer, por desfecho (`apply`, `duplicate`, `buffer`, `discard`, `plan_mismatch`, `tombstone`, `cache_duplicate`, `cache_aborted`) |
| `nexus_events_drained_total` | counter | Eventos do buffer aplicados quando o antecessor chegou |
| `nexus_dlq_messages_total{code}` | counter | Mensagens confirmadas pela DLQ, por codigo |
| `nexus_consumer_retries_total` | counter | Falhas transitorias retentadas no lugar |
| `nexus_event_settle_seconds` | histogram | Do primeiro processamento ate o evento ser resolvido, retries incluidos |
| `nexus_consumer_lag{topic, partition}` | gauge | Registros atras do high watermark, apos cada lote |
| `nexus_seq_cache_errors_total{op}` | counter | Falhas do cache Redis (cada uma desliga o cache por 5s) |
| `nexus_webhook_deliveries_total{result}` | counter | Resultado de cada tentativa de entrega (`delivered`, `retry_scheduled`, `dead_max_attempts`, `dead_rejected`, `released`) |
| `nexus_webhook_request_seconds` | histogram | Duracao das requisicoes de webhook |
| `nexus_circuit_breaker_state` | gauge | 0 fechado, 1 semiaberto, 2 aberto |
| `nexus_circuit_breaker_transitions_total{to}` | counter | Mudancas de estado do circuit breaker |
| `nexus_outbox_entries{state}` | gauge | Notificacoes nao entregues: `pending` e `dead` (lido do Postgres no scrape) |
| `nexus_pending_events` | gauge | Eventos no buffer de reordenacao (lido do Postgres no scrape) |

Contadores com label nascem em zero, entao os paineis mostram `0` em vez de "sem dados". Um evento so e contado depois de resolvido: um retry nao conta o mesmo evento duas vezes, e um envio para a DLQ so conta depois do ack do broker.

## Quick Start

```bash
# Subir toda a infraestrutura
make up

# Verificar se tudo esta saudavel
make status

# Executar o agent (gera 1 pedido completo)
make agent-run

# Ou gerar carga com falhas simuladas (20 planos) e conferir o desfecho de cada um
make agent-e2e

# Verificar resultado no banco
make db-check

# Abrir dashboards
make grafana-open     # http://localhost:3000 (admin/nexus)
make jaeger-open      # http://localhost:16686
make prometheus-open  # http://localhost:9095
```

## Testes

### Integracao Continua

O workflow `.github/workflows/ci.yml` roda em todo push, em qualquer branch:

| Job | O que verifica |
|---|---|
| **Server (Go)** | `gofmt`, `go mod tidy` sem diff, `go vet` e `go test -race` com um Postgres 16 real; com `REQUIRE_INTEGRATION=1`, um teste de integracao sem banco falha em vez de ser pulado |
| **Agent (Python)** | `pytest` do Agent Planner |
| **E2E + Chaos** | sobe a stack com `docker compose`, roda o e2e (50 planos), o agent com falhas simuladas (40 planos, conferidos por `scripts/check_agent_outcomes.py`), os 5 cenarios de chaos e `scripts/check_metrics.py` (metricas coerentes com o que rodou, scrape do Prometheus, todas as queries do dashboard com dados, dashboard e datasource provisionados no Grafana); so roda se os dois jobs acima passarem |

Um PR so deve ser aberto com o CI verde no ultimo commit.

### Localmente

```bash
# Testes Go sem infra (integracao e pulada)
make server-test

# Todos os testes Go contra o Postgres do compose (requer infra up; cada teste usa um schema isolado)
make server-test-integration

# Ou localmente, contra qualquer Postgres 16:
cd server && POSTGRES_DSN="postgres://user:pass@localhost:5432/db?sslmode=disable" go test -race ./...

# Testes unitarios do Agent Python
make agent-test

# Demo E2E: envia 10 planos e valida Exactly-Once
make demo-e2e

# Demo E2E com 100 planos
make demo-e2e PLANS=100

# Agent com falhas simuladas + conferencia no Postgres e no webhook sink (requer `make chaos-deps`)
make agent-e2e

# Chaos tests: sequence gaps, Redis restart (sem reenvio), zombie events, mensagens envenenadas (DLQ)
# e entrega de webhooks com receptor instavel
# Requer `make chaos-deps` e acesso ao Docker (o cenario Redis derruba o container nexus-redis)
make chaos-test
```

## Simulacao de Falhas no Agent

Por padrao o agent gera o caminho feliz. Com as taxas de simulacao, o estoque falha ou o pagamento e recusado em uma fracao dos planos, que terminam em `ABORT_PLAN` com `abort_code` `inventory_failed` ou `payment_rejected`:

| Desfecho | Eventos publicados | Estado no Postgres | Notificacoes (outbox) |
|---|---|---|---|
| completo | seq 1..5 | `completed`, seq 5 | 5 |
| estoque indisponivel | `OrderCreated`, `ABORT_PLAN` (`aborted_at_seq` 1) | `aborted`, seq 1 | 2 |
| pagamento recusado | `OrderCreated`, `InventoryValidated`, `ABORT_PLAN` (`aborted_at_seq` 2) | `aborted`, seq 2 | 3 |

Um pagamento recusado nao gera `PaymentProcessed`: o server marcaria o pedido como `payment_processed`.

```bash
cd agent
python -m src.main --orders 50 \
  --inventory-failure-rate 0.2 --payment-rejection-rate 0.25 \
  --step-delay-ms 200 --seed demo --report ../agent-report.json
```

| Flag | Env (default) | Descricao |
|---|---|---|
| `--orders N` | | Planos a gerar; cada um com um `order_id` novo |
| `--inventory-failure-rate` | `SIM_INVENTORY_FAILURE_RATE` (0) | Fracao dos planos em que o estoque falha |
| `--payment-rejection-rate` | `SIM_PAYMENT_REJECTION_RATE` (0) | Fracao dos planos que chegam ao pagamento e tem o pagamento recusado |
| `--step-delay-ms` | `SIM_STEP_DELAY_MS` (0) | Atraso entre eventos do mesmo plano |
| `--seed` | aleatoria | Reproduz as mesmas decisoes: o plano na posicao `i` usa a seed `<seed>:<i>` |
| `--report` | | Grava o desfecho de cada plano (eventos, `last_seq`, seed do plano) em JSON |

Cada evento e publicado assim que o node do grafo o gera, e o agent espera o ack do broker antes de seguir: se a publicacao falhar, o agent para, grava o relatorio parcial e sai com codigo 1, em vez de deixar o plano pela metade sem aviso.

`scripts/check_agent_outcomes.py agent-report.json` confere cada plano do relatorio no Postgres (status, `last_seq_processed`, `plan_id`, uma entrada do outbox por evento aplicado, todas entregues) e no webhook sink (cada entrega aceita uma vez e em ordem), e exige que cada tipo de falha com taxa > 0 tenha acontecido. `make agent-e2e` roda os dois (`ORDERS`, `INV_RATE`, `PAY_RATE`, `SEED` e `DELAY_MS` ajustam a simulacao).

## LangGraph Studio (Visualizacao do Grafo)

O Agent usa LangGraph para orquestrar a geracao de eventos. Voce pode visualizar e depurar o grafo interativamente via LangGraph Studio.

### Setup (uma vez)

```bash
cd agent
python3.13 -m venv .venv
.venv/bin/pip install ".[dev]"
```

### Rodar

```bash
make agent-studio
# Abre http://127.0.0.1:2024 (API) e Studio UI no browser
```

### Exemplo de Input (colar no Studio)

**Fluxo completo (happy path):**

```json
{
  "order_id": "550e8400-e29b-41d4-a716-446655440000",
  "plan_id": "plan_demo_001",
  "user_id": "usr_debora",
  "items": [{"product_id": "prod_abc", "quantity": 2, "unit_price": 49.90}],
  "total_amount": 99.80,
  "currency": "BRL",
  "current_seq": 0,
  "events": [],
  "status": "idle",
  "abort_reason": ""
}
```

**Fluxo de abort (items vazios):**

```json
{
  "order_id": "550e8400-e29b-41d4-a716-446655440000",
  "plan_id": "plan_demo_002",
  "user_id": "usr_debora",
  "items": [],
  "total_amount": 0,
  "currency": "BRL",
  "current_seq": 0,
  "events": [],
  "status": "idle",
  "abort_reason": ""
}
```

**Falha simulada (pagamento recusado):** adicione o campo `simulation` a qualquer um dos inputs acima. As decisoes dependem so da seed, entao o mesmo input sempre segue o mesmo caminho; `plan_id` e `order_id` nao influenciam.

```json
{
  "simulation": {"inventory_failure_rate": 0, "payment_rejection_rate": 1, "seed": "studio"}
}
```

> Requer `LANGSMITH_API_KEY` no `agent/.env`. Crie uma conta gratuita em [LangSmith](https://smith.langchain.com/) e copie a API key.

## Estrutura do Projeto

```
.
├── agent/                   # Agent Python (LangGraph)
│   ├── src/
│   │   ├── planner/         # Grafo LangGraph (nodes, state, simulacao de falhas)
│   │   ├── producer/        # Producer Kafka (ack por evento)
│   │   ├── models/          # Pydantic models (EventEnvelope)
│   │   ├── runner.py        # Executa o grafo e publica cada evento ao ser gerado
│   │   └── config.py
│   └── tests/               # pytest (unit tests)
├── server/                  # Server Go (Event Processor)
│   ├── cmd/server/          # Entrypoint
│   ├── internal/
│   │   ├── consumer/        # Kafka consumer (retry, commit, DLQ)
│   │   ├── sequencing/      # Regras puras de ordenacao (sem I/O)
│   │   ├── redis/           # Cache de sequencia + Lua loader
│   │   ├── db/              # Postgres repository (transacao de sequencia)
│   │   ├── dlq/             # Dead Letter Queue producer
│   │   └── telemetry/       # OpenTelemetry setup
│   ├── pkg/models/          # Shared models
│   └── scripts/lua/         # advance_seq.lua
├── scripts/
│   ├── chaos_test.py        # Chaos testing scenarios
│   ├── e2e_demo.py          # Demo E2E com validacao
│   ├── check_agent_outcomes.py  # Confere o relatorio do agent no Postgres e no webhook sink
│   ├── check_metrics.py     # Valida server -> Prometheus -> dashboard
│   └── webhook_sink.py      # Receptor de webhooks para testes
├── monitoring/grafana/      # Dashboards + provisioning
├── docker-compose.yml
├── init.sql                 # Schema Postgres
└── Makefile                 # Todos os comandos
```

## Makefile Commands

```bash
make help  # Lista todos os comandos disponiveis
```

### Principais

| Comando | Descricao |
|---|---|
| `make up` | Sobe todos os containers |
| `make down` | Derruba containers |
| `make status` | Status dos containers |
| `make agent-run` | Executa o agent (1 pedido) |
| `make agent-simulate` | Gera planos com falhas simuladas e grava `agent-report.json` |
| `make agent-e2e` | `agent-simulate` + conferencia no Postgres e no webhook sink |
| `make server-test` | Testes Go (sem infra) |
| `make agent-test` | Testes unitarios Python |
| `make demo-e2e` | Demo E2E Exactly-Once |
| `make chaos-test` | Chaos tests completos |
| `make webhook-stats` | Entregas recebidas pelo webhook sink, por pedido |
| `make metrics` | Metricas `nexus_*` expostas pelo server |
| `make metrics-check` | Valida server, Prometheus e dashboard (depois de `demo-e2e` e `chaos-test`) |

## Fases do Projeto

| Fase | Descricao | Status |
|---|---|---|
| 1 | Kafka + Redis + Postgres | Completa |
| 2 | Agent Python (LangGraph) | Completa |
| 3 | Server Go (Consumer + Lua) | Completa |
| 4 | Consul + Circuit Breaker | Completa |
| 5 | Observabilidade + Chaos Tests | Completa |
| 6 | Qualidade + Demo E2E | Completa |
| 7 | Correcao das garantias (ADR-004) | Completa |
| 8 | Integracao continua (GitHub Actions) | Completa |
| 9 | Entrega de webhooks (ADR-005) | Completa |
| 10 | Metricas com Prometheus (ADR-006) | Completa |
| 11 | Agent com falhas simuladas | Completa |
