# Quick Tech Spec — Fase 2: O Cerebro Agentico (Python)

## Resumo

Implementar o Agent Planner em Python usando LangGraph, que gera sequencias deterministicas de eventos para o pipeline Exactly-Once do Nexus Event Gateway. O agente recebe um pedido, produz um plano (`plan_id` + sequencia de eventos com `seq_id`), publica no Kafka com ordering key (`order_id`) e headers de tracing (`traceparent`), e suporta cancelamento via evento `ABORT_PLAN`.

O agente roda como container Docker integrado ao `docker-compose.yml` existente.

---

## Contexto

A Fase 1 (completa) entregou:
- Docker Compose com Kafka (KRaft), Redis, Postgres, Consul e Jaeger
- JSON Schemas para todos os eventos (envelope + 6 tipos)
- Tabelas SQL (orders + outbox) com indices
- Script de validacao de conectividade

A Fase 2 cria o **produtor** do pipeline — o Agent que gera e publica eventos.

---

## Tasks e Criterios de Aceite

### Task 2.1: Configurar ambiente Python com Docker

**O que fazer:**
- Criar `agent/` na raiz do projeto com a estrutura:
  ```
  agent/
    Dockerfile
    pyproject.toml
    src/
      __init__.py
      config.py
      main.py
      planner/
        __init__.py
        graph.py        # LangGraph state machine
        nodes.py        # Node functions do grafo
        state.py        # TypedDict com o estado do plano
      producer/
        __init__.py
        kafka_producer.py
      models/
        __init__.py
        events.py       # Pydantic models dos eventos (baseados nos JSON Schemas)
  ```
- `pyproject.toml` com dependencias:
  - `langgraph >= 0.4` — orquestracao do plano
  - `confluent-kafka` — producer Kafka (lib mais estavel para Python producer; franz-go e decisao do consumer Go, nao se aplica aqui)
  - `pydantic >= 2.0` — validacao de eventos contra os schemas
  - `opentelemetry-api`, `opentelemetry-sdk`, `opentelemetry-exporter-otlp` — tracing
  - `python-dotenv` — config

- `Dockerfile` multi-stage:
  ```dockerfile
  FROM python:3.12-slim AS base
  WORKDIR /app
  COPY pyproject.toml .
  RUN pip install --no-cache-dir .
  COPY src/ src/
  CMD ["python", "-m", "src.main"]
  ```

- Adicionar servico `agent` ao `docker-compose.yml`:
  ```yaml
  agent:
    build: ./agent
    container_name: nexus-agent
    depends_on:
      kafka:
        condition: service_healthy
    environment:
      KAFKA_BOOTSTRAP_SERVERS: kafka:29092
      OTEL_EXPORTER_OTLP_ENDPOINT: http://jaeger:4317
    restart: on-failure
  ```

- `config.py` le variaveis de ambiente com defaults para dev local.

**Criterios de Aceite:**
- [ ] `docker compose build agent` compila sem erros
- [ ] `docker compose up agent` starta e conecta ao Kafka (log de conexao visivel)
- [ ] Estrutura de pastas segue o layout acima

---

### Task 2.2: Criar Agent Planner com LangGraph

**O que fazer:**
- Definir o estado do plano em `state.py`:
  ```python
  class PlanState(TypedDict):
      order_id: str           # UUID do pedido
      plan_id: str            # "plan_{uuid4_short}"
      user_id: str
      items: list[dict]
      total_amount: float
      currency: str
      current_seq: int        # Contador de sequencia
      events: list[dict]      # Eventos gerados (buffer antes de publish)
      status: str             # "planning" | "publishing" | "done" | "aborted"
  ```

- Implementar o grafo LangGraph em `graph.py` com os seguintes nodes:

  ```
  [START] -> generate_plan -> create_order_event -> create_inventory_event
          -> create_payment_event -> create_shipping_event
          -> create_completion_event -> publish_events -> [END]
  ```

  Cada node:
  1. Incrementa `current_seq`
  2. Gera o evento correspondente com `plan_id`, `seq_id`, `order_id`, `timestamp`
  3. Popula o campo `data` conforme o JSON Schema do tipo
  4. Append em `events[]`

- Conditional edge: se qualquer node falhar na validacao, redireciona para um node `abort_plan` que gera o evento `ABORT_PLAN`.

- O `plan_id` e gerado como `plan_{uuid4_hex[:12]}` (ex: `plan_a1b2c3d4e5f6`).

- Eventos gerados seguem rigorosamente o `envelope.schema.json` e os schemas de `data` da Fase 1.

**Criterios de Aceite:**
- [ ] Grafo executa e gera 5 eventos em sequencia (seq_id 1 a 5) para um pedido
- [ ] Cada evento valida contra o JSON Schema correspondente (Pydantic)
- [ ] `plan_id` e unico por execucao
- [ ] Condicional de abort funciona — simular falha gera `ABORT_PLAN`

---

### Task 2.3: Integrar Producer Kafka com ordering e tracing

**O que fazer:**
- Implementar `kafka_producer.py`:
  - Criar `confluent_kafka.Producer` com config:
    ```python
    {
        "bootstrap.servers": config.KAFKA_BOOTSTRAP_SERVERS,
        "acks": "all",
        "enable.idempotence": True,
        "retries": 5,
        "max.in.flight.requests.per.connection": 1,
    }
    ```
  - Metodo `publish_event(event: dict)`:
    - Topic: `orders`
    - Key: `event["order_id"]` (garante ordering por pedido na mesma particao)
    - Value: JSON serializado do evento
    - Headers: `[("traceparent", traceparent_value)]`
  - Metodo `publish_plan(events: list[dict])`:
    - Publica todos os eventos de um plano em sequencia
    - Callback de delivery para log/metricas
    - `flush()` ao final para garantir entrega

- No node `publish_events` do LangGraph, chamar `publish_plan(state["events"])`.

- Gerar `traceparent` seguindo W3C Trace Context:
  ```
  traceparent: 00-{trace_id_hex32}-{span_id_hex16}-01
  ```
  Usar OpenTelemetry SDK para gerar trace_id e span_id reais (nao hardcoded).

**Criterios de Aceite:**
- [ ] Eventos chegam no topic `orders` do Kafka (verificavel via `kafka-console-consumer`)
- [ ] Todos os eventos de um plano tem a mesma key (`order_id`)
- [ ] Header `traceparent` presente em cada mensagem
- [ ] `acks=all` e `enable.idempotence=True` configurados

---

### Task 2.4: Implementar ABORT_PLAN (Tombstone)

**O que fazer:**
- Criar node `abort_plan` no grafo LangGraph:
  - Gera evento `ABORT_PLAN` conforme `abort-plan.schema.json`
  - **Sem** `seq_id` (conforme envelope schema — condicional `if/then`)
  - Popula `reason`, `abort_code` e `aborted_at_seq` (ultimo seq processado)
  - Publica imediatamente no Kafka (nao espera buffer)

- Triggers de abort (conditional edges no grafo):
  - Validacao de dados do pedido falha (items vazios, total <= 0)
  - Qualquer erro inesperado nos nodes

- Apos publicar `ABORT_PLAN`, estado muda para `"aborted"` e o grafo termina.

**Criterios de Aceite:**
- [ ] Evento `ABORT_PLAN` publicado no topic `orders` com mesma key (`order_id`)
- [ ] Evento nao contem `seq_id`
- [ ] `abort_code` e um dos valores validos do schema
- [ ] `aborted_at_seq` reflete o ultimo seq gerado antes do abort

---

## Entrypoint e Modo de Operacao

O `main.py` opera em **dois modos**:

1. **CLI (default no dev):** Recebe um JSON de pedido via stdin ou argumento, executa o plano uma vez e sai.
   ```bash
   docker compose run agent python -m src.main --order '{"user_id": "usr_123", "items": [...]}'
   ```

2. **Loop (futuro):** Um endpoint HTTP ou consumer que recebe pedidos. **Nao implementar agora** — o modo CLI e suficiente para a Fase 2.

---

## Decisoes Tecnicas

| Decisao | Justificativa |
|---|---|
| `confluent-kafka` no Python (vs `kafka-python`) | `kafka-python` esta abandonado. `confluent-kafka` e a lib oficial e suporta idempotent producer. ADR-001 (franz-go) aplica-se apenas ao consumer Go. |
| Pydantic para validacao de eventos | Validacao em runtime contra os JSON Schemas ja definidos. Type safety no Python. |
| Eventos gerados em buffer antes de publish | Permite validar o plano completo antes de enviar. Se abort, nenhum evento parcial vai pro Kafka. |
| `traceparent` via OpenTelemetry SDK | Gera trace/span IDs reais, nao mocks. Prepara para Fase 5 (observabilidade completa). |
| Container Docker com `depends_on: kafka` | Garante que o agent so starta quando Kafka esta healthy. |

---

## Validacao da Spec

- [ ] Todas as tasks mapeiam diretamente para Task 2.1–2.4 do `docs/tasks.md`
- [ ] Eventos seguem os JSON Schemas da Fase 1 (`schemas/events/`)
- [ ] Nenhuma mudanca nos servicos existentes do `docker-compose.yml` (apenas adicao)
- [ ] Agent funciona isolado — nao depende do consumer Go (Fase 3) existir
