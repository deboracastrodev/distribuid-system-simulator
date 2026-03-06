# Documentação de Conclusão: Fase 2 - O Cérebro Agêntico (Python)

Esta documentação detalha a implementação da Fase 2 do Nexus Event Gateway, responsável pela geração e orquestração de planos de eventos utilizando LangGraph e Python.

## 🚀 Visão Geral
O Agente Planner foi desenvolvido como o componente central de inteligência do sistema. Ele recebe um pedido (Order) e gera uma sequência determinística de eventos que devem ser processados pelo consumidor Go, garantindo a integridade do fluxo de negócio desde a criação até a entrega.

## 🛠️ Componentes Implementados

### 1. Ambiente e Dependências (Task 2.1)
- Configurado ambiente Python 3.12 com `pyproject.toml`.
- Dependências principais:
  - `langgraph`: Orquestração de grafos de estado.
  - `confluent-kafka`: Cliente Kafka robusto e performático.
  - `pydantic`: Validação rigorosa de contratos de dados (Envelopes).
  - `opentelemetry`: Suporte nativo a Tracing Distribuído.

### 2. Agent Planner com LangGraph (Task 2.2)
- **Grafo de Estado:** Implementado em `src/planner/graph.py`.
- **Identificadores Únicos:** Cada plano gera um `plan_id` único e cada evento possui um `seq_id` incremental.
- **Fluxo de Eventos:**
  1. `OrderCreated` (seq: 1)
  2. `InventoryValidated` (seq: 2)
  3. `PaymentProcessed` (seq: 3)
  4. `OrderShipped` (seq: 4)
  5. `OrderCompleted` (seq: 5)

### 3. Integração Producer Kafka (Task 2.3)
- **Ordering Key:** Uso do `order_id` como chave de partição no Kafka para garantir ordenação por pedido.
- **Idempotência:** Configurado `enable.idempotence=True` no producer.
- **Headers de Trace:** Injeção automática do header `traceparent` (W3C) em todos os eventos para observabilidade.

### 4. Evento ABORT_PLAN (Task 2.4)
- Implementado como um "Tombstone" lógico.
- Utilizado para invalidar planos no Redis quando uma validação falha no início do grafo.
- Schema definido em `src/models/events.py` seguindo os padrões do projeto.

## 🔬 Validação Técnica
- **Conformidade com ADRs:** O código segue estritamente as definições de `docs/blueprint-arquitetura.md`.
- **Tracing:** Integração com Jaeger via OTLP validada no entrypoint `main.py`.
- **CLI Robustez:** Suporte a entrada via JSON inline ou arquivo (`@file.json`).

## 📁 Arquivos Principais
- `agent/src/main.py`: Entrypoint e configuração de Tracing.
- `agent/src/planner/graph.py`: Definição da lógica do grafo.
- `agent/src/planner/nodes.py`: Funções de geração de cada evento.
- `agent/src/producer/kafka_producer.py`: Lógica de publicação e headers.
- `agent/src/models/events.py`: Modelos Pydantic (Envelopes e Data).

---
*Documentação gerada como parte da revisão adversarial da Fase 2.*
