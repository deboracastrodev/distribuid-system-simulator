# Nexus Event Gateway

Simulador de sistema distribuido com garantias **Exactly-Once** para processamento de eventos de pedidos.

## Arquitetura

```mermaid
graph LR
    A[Agent Python<br/>LangGraph] -->|Kafka| B[Server Go<br/>Event Processor]
    B -.->|cache| C[(Redis<br/>Lua Script)]
    B --> D[(Postgres<br/>Source of Truth)]
    B --> E[DLQ<br/>Dead Letter Queue]
    F[Consul] -.->|Service Discovery<br/>Circuit Breaker| B
    G[Jaeger] -.->|Traces OTLP| A & B
    H[Grafana] -.->|Dashboards| G
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
    loop Para cada evento (seq 1..5)
        Agent->>Kafka: EventEnvelope {plan_id, seq_id, event_type}
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

## Quick Start

```bash
# Subir toda a infraestrutura
make up

# Verificar se tudo esta saudavel
make status

# Executar o agent (gera 1 pedido completo)
make agent-run

# Verificar resultado no banco
make db-check

# Abrir dashboards
make grafana-open   # http://localhost:3000 (admin/nexus)
make jaeger-open    # http://localhost:16686
```

## Testes

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

# Chaos tests: sequence gaps, Redis restart (sem reenvio), zombie events e mensagens envenenadas (DLQ)
# Requer `make chaos-deps` e acesso ao Docker (o cenario Redis derruba o container nexus-redis)
make chaos-test
```

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

> Requer `LANGSMITH_API_KEY` no `agent/.env`. Crie uma conta gratuita em [LangSmith](https://smith.langchain.com/) e copie a API key.

## Estrutura do Projeto

```
.
├── agent/                   # Agent Python (LangGraph)
│   ├── src/
│   │   ├── planner/         # Grafo LangGraph (nodes, state)
│   │   ├── models/          # Pydantic models (EventEnvelope)
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
│   └── e2e_demo.py          # Demo E2E com validacao
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
| `make server-test` | Testes Go (sem infra) |
| `make agent-test` | Testes unitarios Python |
| `make demo-e2e` | Demo E2E Exactly-Once |
| `make chaos-test` | Chaos tests completos |

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
