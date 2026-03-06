---
title: 'JSON Schemas de Eventos + Melhorias no init.sql'
slug: 'event-schemas-sql-improvements'
created: '2026-03-01'
status: 'implemented'
tasks_covered: ['1.4', '1.5']
tech_stack: [json-schema, postgresql, kafka]
files_to_create: [schemas/events/envelope.schema.json, schemas/events/order-created.schema.json, schemas/events/inventory-validated.schema.json, schemas/events/payment-processed.schema.json, schemas/events/order-shipped.schema.json, schemas/events/order-completed.schema.json, schemas/events/abort-plan.schema.json]
files_to_modify: [init.sql]
code_patterns: ['envelope wrapper com headers padrao', 'plan_id + seq_id em todos os eventos', 'ordering key = order_id', 'traceparent header para OpenTelemetry']
---

# Tech-Spec: JSON Schemas de Eventos + Melhorias no init.sql

**Created:** 2026-03-01

> Cobre Tasks 1.4 e 1.5 da Fase 1 do [tasks.md](../../docs/tasks.md).

## Overview

### Problem Statement

O Nexus Event Gateway nao tem contrato formal para os eventos que trafegam pelo Kafka. Sem JSON Schemas, o Agent Python e o Server Go nao tem referencia compartilhada para validar payloads — qualquer typo ou campo faltando so seria detectado em runtime. Alem disso, o `init.sql` existente precisa de melhorias para refletir os status reais do fluxo de pedidos e garantir integridade referencial.

### Solution

1. **Task 1.4:** Criar JSON Schemas (Draft 2020-12) para todos os eventos do fluxo de pedidos, usando um envelope padrao que encapsula metadata comum (plan_id, seq_id, traceparent, timestamps).
2. **Task 1.5:** Melhorar o `init.sql` com CHECK constraints para status validos, coluna `event_type` na tabela outbox, e indice adicional para o Outbox Poller.

### Scope

**In Scope:**
- Envelope schema padrao (metadata comum a todos os eventos)
- 6 event schemas: `OrderCreated`, `InventoryValidated`, `PaymentProcessed`, `OrderShipped`, `OrderCompleted`, `ABORT_PLAN`
- Melhorias no `init.sql` (constraints, indices, coluna event_type)
- Diretorio `schemas/events/` na raiz do projeto

**Out of Scope:**
- Validacao em runtime (sera implementada nas Fases 2 e 3 quando Agent e Server existirem)
- Schemas para respostas de webhook (Task 3.7)
- Schemas para eventos da DLQ (Task 3.5)
- Versionamento de schemas (Schema Registry) — overkill para o escopo atual

## Context for Development

### Fluxo de Eventos do Pedido (Happy Path)

```
Agent Python                    Kafka                     Go Server
    |                             |                           |
    |-- OrderCreated (seq:1) ---->|                           |
    |                             |--- OrderCreated --------->|
    |                             |                           |-- Redis Lua check
    |                             |                           |-- Postgres UPDATE
    |                             |                           |
    |-- InventoryValidated (2) -->|                           |
    |                             |--- InventoryValidated --->|
    |                             |                           |
    |-- PaymentProcessed (3) ---->|                           |
    |                             |--- PaymentProcessed ----->|
    |                             |                           |
    |-- OrderShipped (seq:4) ---->|                           |
    |                             |--- OrderShipped --------->|
    |                             |                           |
    |-- OrderCompleted (seq:5) -->|                           |
    |                             |--- OrderCompleted ------->|
    |                             |                           |-- Webhook notify
```

**Abort Path:** A qualquer momento, o Agent pode enviar `ABORT_PLAN` (Tombstone) que invalida o `plan_id` no Redis e limpa buffers.

### Kafka Headers (padrao para todos os eventos)

Estes headers sao injetados pelo Producer Python e propagados pelo Consumer Go:

| Header | Tipo | Exemplo | Proposito |
| --- | --- | --- | --- |
| `traceparent` | string | `00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01` | W3C Trace Context para OpenTelemetry |
| `event_type` | string | `OrderCreated` | Tipo do evento (redundante com payload, mas util para routing sem deserializar) |
| `plan_id` | string | `plan_77` | ID do plano de execucao gerado pelo Agent |

**Kafka Key (ordering key):** `order_id` — garante que todos os eventos de um mesmo pedido vao para a mesma particao, preservando ordem.

### Codebase Patterns

- Projeto usa `docker-compose.yml` na raiz com mount de `./init.sql` para Postgres
- Sem tooling de validacao JSON Schema no projeto ainda (sera adicionado nas Fases 2/3)
- Convencao de nomes: snake_case para campos JSON, PascalCase para event types
- IDs usam UUID v4 (consistente com `init.sql` existente)

### Files to Reference

| File | Purpose |
| ---- | ------- |
| `init.sql` | Script SQL atual — sera modificado |
| `docs/blueprint-arquitetura.md` | Blueprint com fluxo exactly-once e Lua script |
| `docs/tasks.md` | Tasks 1.4 e 1.5 |
| `docker-compose.yml` | Mount do init.sql para Postgres |

### Technical Decisions

- **JSON Schema Draft 2020-12:** Padrao mais recente e estavel. Suportado por bibliotecas em Python (`jsonschema`) e Go (`gojsonschema`).
- **Envelope Pattern:** Todo evento e encapsulado em um envelope com metadata comum. O payload especifico fica no campo `data`. Isso permite que o Consumer Go faca routing e validacao de sequencia SEM precisar conhecer o schema especifico de cada evento.
- **`seq_id` comeca em 1:** Consistente com o Lua script do blueprint (`last_seq + 1`). Sequencia e por `plan_id`, nao por `order_id`.
- **Timestamps em ISO 8601 UTC:** `2026-03-01T15:30:00Z` — sem timezone offset, sempre UTC.
- **`ABORT_PLAN` nao tem `seq_id`:** Tombstones invalidam o plano inteiro, nao tem posicao na sequencia.
- **Kafka topic:** `orders` (singular, conforme diagrama do blueprint).
- **Melhorias no SQL sao aditivas:** Nenhuma coluna existente sera removida ou renomeada. Apenas adicoes (CHECK, coluna, indice).

## Implementation Plan

### Tasks

- [ ] Task 1: Criar diretorio `schemas/events/` e o envelope schema
  - File: `schemas/events/envelope.schema.json`
  - Action: Criar o schema base que todo evento segue. Campos:
    ```json
    {
      "$schema": "https://json-schema.org/draft/2020-12/schema",
      "$id": "envelope.schema.json",
      "title": "Nexus Event Envelope",
      "description": "Envelope padrao para todos os eventos do Nexus Event Gateway",
      "type": "object",
      "properties": {
        "event_id": {
          "type": "string",
          "format": "uuid",
          "description": "ID unico do evento (UUID v4)"
        },
        "event_type": {
          "type": "string",
          "enum": ["OrderCreated", "InventoryValidated", "PaymentProcessed", "OrderShipped", "OrderCompleted", "ABORT_PLAN"],
          "description": "Tipo do evento"
        },
        "plan_id": {
          "type": "string",
          "pattern": "^plan_[a-zA-Z0-9_-]+$",
          "description": "ID do plano de execucao gerado pelo Agent"
        },
        "seq_id": {
          "type": "integer",
          "minimum": 1,
          "description": "Numero sequencial do evento dentro do plano (ausente em ABORT_PLAN)"
        },
        "order_id": {
          "type": "string",
          "format": "uuid",
          "description": "ID do pedido (usado como Kafka key para ordering)"
        },
        "timestamp": {
          "type": "string",
          "format": "date-time",
          "description": "Timestamp de criacao do evento em ISO 8601 UTC"
        },
        "data": {
          "type": "object",
          "description": "Payload especifico do tipo de evento"
        }
      },
      "required": ["event_id", "event_type", "plan_id", "order_id", "timestamp", "data"]
    }
    ```
  - Notes: `seq_id` e required para todos os tipos EXCETO `ABORT_PLAN`. Isso sera tratado com `if/then` no schema. O campo `data` contem o payload especifico — cada event schema define o que vai dentro de `data`.

- [ ] Task 2: Criar schemas para cada tipo de evento
  - Files:
    - `schemas/events/order-created.schema.json`
    - `schemas/events/inventory-validated.schema.json`
    - `schemas/events/payment-processed.schema.json`
    - `schemas/events/order-shipped.schema.json`
    - `schemas/events/order-completed.schema.json`
    - `schemas/events/abort-plan.schema.json`
  - Action: Cada arquivo define o schema do campo `data` para o respectivo evento:

    **OrderCreated** (`seq_id: 1` — sempre o primeiro):
    ```json
    {
      "data": {
        "user_id": "string (required)",
        "items": [
          {
            "product_id": "string (required)",
            "quantity": "integer, minimum 1 (required)",
            "unit_price": "number, minimum 0 (required)"
          }
        ],
        "total_amount": "number, minimum 0 (required)",
        "currency": "string, default 'BRL' (required)"
      }
    }
    ```

    **InventoryValidated** (`seq_id: 2`):
    ```json
    {
      "data": {
        "validated": "boolean (required)",
        "items_status": [
          {
            "product_id": "string (required)",
            "available": "boolean (required)",
            "reserved_quantity": "integer, minimum 0 (required)"
          }
        ]
      }
    }
    ```

    **PaymentProcessed** (`seq_id: 3`):
    ```json
    {
      "data": {
        "payment_id": "string, uuid (required)",
        "method": "string, enum [credit_card, debit_card, pix, boleto] (required)",
        "status": "string, enum [approved, rejected] (required)",
        "amount": "number, minimum 0 (required)",
        "currency": "string, default 'BRL' (required)"
      }
    }
    ```

    **OrderShipped** (`seq_id: 4`):
    ```json
    {
      "data": {
        "tracking_code": "string (required)",
        "carrier": "string (required)",
        "estimated_delivery": "string, format date (required)"
      }
    }
    ```

    **OrderCompleted** (`seq_id: 5` — sempre o ultimo):
    ```json
    {
      "data": {
        "final_status": "string, enum [delivered, cancelled] (required)",
        "completed_at": "string, format date-time (required)"
      }
    }
    ```

    **ABORT_PLAN** (sem `seq_id`):
    ```json
    {
      "data": {
        "reason": "string (required)",
        "abort_code": "string, enum [inventory_failed, payment_rejected, timeout, manual] (required)",
        "aborted_at_seq": "integer, minimum 0 (optional — ultimo seq processado antes do abort)"
      }
    }
    ```

  - Notes: Cada schema usa `$ref` para referenciar o envelope base onde fizer sentido. Os schemas sao autodocumentados — descricao em cada campo. Sequencia fixa (1-5) e a sequencia esperada no happy path, mas o Agent pode gerar planos com menos steps (ex: se inventario falha, nao gera PaymentProcessed em diante — envia ABORT_PLAN).

- [ ] Task 3: Melhorar `init.sql` com constraints e novos campos
  - File: `init.sql`
  - Action: Aplicar as seguintes melhorias aditivas:

    1. **Tabela `orders`** — adicionar CHECK constraint para status validos:
       ```sql
       -- Adicionar CHECK no status com os valores reais do fluxo
       ALTER TABLE orders ADD CONSTRAINT chk_order_status
         CHECK (status IN ('pending', 'inventory_validated', 'payment_processed',
                           'shipped', 'completed', 'cancelled', 'aborted'));
       ```
       Nao usar ALTER — como e script de init, incorporar o CHECK direto no CREATE TABLE.

       Status validos e suas transicoes:
       - `pending` → OrderCreated recebido
       - `inventory_validated` → InventoryValidated (validated=true)
       - `payment_processed` → PaymentProcessed (status=approved)
       - `shipped` → OrderShipped
       - `completed` → OrderCompleted (final_status=delivered)
       - `cancelled` → OrderCompleted (final_status=cancelled)
       - `aborted` → ABORT_PLAN recebido

    2. **Tabela `outbox`** — adicionar coluna `event_type` e indice para o poller:
       ```sql
       -- Adicionar coluna event_type para facilitar routing do Outbox Poller
       event_type VARCHAR(50) NOT NULL,

       -- Indice para o Outbox Poller buscar eventos nao processados
       CREATE INDEX idx_outbox_unprocessed ON outbox(processed, created_at)
         WHERE processed = FALSE;
       ```

    3. **Tabela `orders`** — adicionar coluna `plan_id` para rastreabilidade:
       ```sql
       plan_id VARCHAR(100),  -- ID do plano de execucao ativo (nullable — sem plano ate primeiro evento)
       ```

  - Notes: Todas as mudancas sao incorporadas diretamente no CREATE TABLE (nao como ALTERs separados), ja que `init.sql` roda apenas na criacao do banco. O campo `plan_id` na tabela `orders` permite que o Go server valide se um evento pertence ao plano ativo do pedido. O indice parcial `WHERE processed = FALSE` e otimizado para o Outbox Poller que so busca eventos pendentes.

- [ ] Task 4: Atualizar `docs/tasks.md` marcando Tasks 1.4 e 1.5 como concluidas
  - File: `docs/tasks.md`
  - Action: Trocar `- [ ]` por `- [x]` nas Tasks 1.4 e 1.5

### Ordem de Execucao e Verificacao por Task

1. **Task 1** → verificar: arquivo criado, JSON valido (`python3 -m json.tool schemas/events/envelope.schema.json`)
2. **Task 2** → verificar: todos os 6 arquivos criados, JSONs validos
3. **Task 3** → verificar: `make down-clean && make up`, conectar no Postgres e confirmar schema (`\d orders`, `\d outbox`)
4. **Task 4** → verificar: tasks.md atualizado

## Acceptance Criteria

- [ ] AC 1: Given o diretorio `schemas/events/`, when listar arquivos, then deve conter 7 schemas: envelope + 6 event types.
- [ ] AC 2: Given cada arquivo `.schema.json`, when validar com `python3 -m json.tool`, then deve ser JSON valido sem erros de sintaxe.
- [ ] AC 3: Given o envelope schema, when inspecionar, then deve conter campos `event_id`, `event_type`, `plan_id`, `seq_id`, `order_id`, `timestamp` e `data`.
- [ ] AC 4: Given o schema `abort-plan.schema.json`, when inspecionar, then `seq_id` NAO deve ser required (diferente dos demais eventos).
- [ ] AC 5: Given o `init.sql` atualizado, when executar `make down-clean && make up`, then o Postgres deve inicializar sem erros.
- [ ] AC 6: Given o Postgres inicializado, when consultar `\d orders`, then deve mostrar CHECK constraint no campo `status` e coluna `plan_id`.
- [ ] AC 7: Given o Postgres inicializado, when consultar `\d outbox`, then deve mostrar coluna `event_type` e indice `idx_outbox_unprocessed`.
- [ ] AC 8: Given um payload de exemplo `OrderCreated`, when comparar com o schema, then todos os campos obrigatorios devem estar presentes e com tipos corretos.

## Additional Context

### Mapa de Sequencia por Event Type

| seq_id | event_type | Status resultante no orders | Proximo esperado |
| --- | --- | --- | --- |
| 1 | OrderCreated | pending | InventoryValidated |
| 2 | InventoryValidated | inventory_validated | PaymentProcessed |
| 3 | PaymentProcessed | payment_processed | OrderShipped |
| 4 | OrderShipped | shipped | OrderCompleted |
| 5 | OrderCompleted | completed/cancelled | (fim) |
| - | ABORT_PLAN | aborted | (fim) |

### Exemplo de Evento Completo (como chega no Kafka)

**Kafka Record:**
- **Topic:** `orders`
- **Key:** `550e8400-e29b-41d4-a716-446655440000` (order_id)
- **Headers:**
  - `traceparent`: `00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01`
  - `event_type`: `OrderCreated`
  - `plan_id`: `plan_77`
- **Value (JSON):**
```json
{
  "event_id": "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
  "event_type": "OrderCreated",
  "plan_id": "plan_77",
  "seq_id": 1,
  "order_id": "550e8400-e29b-41d4-a716-446655440000",
  "timestamp": "2026-03-01T15:30:00Z",
  "data": {
    "user_id": "user_42",
    "items": [
      {
        "product_id": "prod_abc",
        "quantity": 2,
        "unit_price": 49.90
      }
    ],
    "total_amount": 99.80,
    "currency": "BRL"
  }
}
```

### Dependencies

- Nenhuma dependencia nova — JSON Schemas sao arquivos estaticos
- Para Task 3 (SQL): Docker Compose ja configurado e funcional

### Notes

- Os schemas servem como **contrato** entre o Agent Python (producer) e o Server Go (consumer). Ambos devem validar contra estes schemas.
- A sequencia 1-5 e o happy path. O Agent pode gerar planos com menos steps quando um step intermediario falha (ex: inventario insuficiente → ABORT_PLAN apos seq 2).
- O campo `currency` usa BRL como default, mas suporta outros valores para extensibilidade futura.
- O `plan_id` no formato `plan_{id}` facilita debugging visual em logs e Redis keys.
