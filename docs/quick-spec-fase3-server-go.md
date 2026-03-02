# Quick Tech Spec — Fase 3: O Coração de Go (Server)

## Resumo
Implementar o servidor de processamento de eventos em Go, responsável por consumir eventos do Kafka, validar a sequência atômica via Redis + Lua, persistir o estado no Postgres usando o Outbox Pattern e notificar clientes via Webhooks. O objetivo central é garantir a semântica **Exactly-Once**.

---

## Contexto e ADRs
- **ADR-001:** Uso de `franz-go` para alta performance e pure-Go Kafka consumption.
- **ADR-002:** Uso de `Consul` para Service Discovery e Health Checks.
- **ADR-003:** Redis + Lua para garantir atomicidade no controle de sequência (`seq_id`).

---

## Tasks e Critérios de Aceite

### Task 3.1: Inicializar Módulo Go e Consumidor Kafka
**O que fazer:**
- Criar diretório `server/` com `go mod init github.com/user/nexus-server`.
- Configurar consumidor `franz-go` para o tópico `orders`.
- Implementar lógica de retry exponencial e tratamento de sinais (Graceful Shutdown).
- Integrar OpenTelemetry para extrair o `traceparent` do header da mensagem Kafka.

**Critérios de Aceite:**
- [x] Servidor conecta ao Kafka e loga o recebimento de mensagens.
- [x] Contexto de trace é propagado corretamente para os logs do servidor.
- [x] Graceful shutdown implementado com sucesso.

### Task 3.2: Script Lua para Validação Atômica (Check-and-Set)
**O que fazer:**
- Criar `scripts/lua/check_and_set_seq.lua`.
- Lógica: Recebe `plan_id` e `seq_id`. Se `seq_id == last_seq + 1`, atualiza e retorna OK. Se for duplicado, retorna `DUPLICATE`. Se for fora de ordem, retorna `OUT_OF_ORDER`.
- Implementar testes isolados para o script Lua.

**Critérios de Aceite:**
- [x] Script Lua garante atomicidade (testado com concorrência).
- [x] Retorna códigos de erro claros para duplicatas e gaps de sequência.
- [x] Suporte a `ABORT_PLAN` integrado no script.

### Task 3.3: Waiting Room (Buffer) no Redis
**O que fazer:**
- Implementar lógica no Go: se o Lua retornar `OUT_OF_ORDER`, armazenar o evento em uma Hash/ZSet no Redis com a chave do `plan_id`.
- Configurar **TTL de 1 hora** para eventos no buffer.
- Criar um worker/watcher que verifica expiração e envia para a DLQ (Task 3.5).

**Critérios de Aceite:**
- [x] Eventos futuros são bufferizados e processados assim que o gap é preenchido.
- [x] TTL é respeitado e limpa o Redis automaticamente.
- [ ] Worker de expiração proativa para DLQ (Pendente: Atualmente depende de expiração passiva do Redis).

### Task 3.4: Transação ACID no Postgres (Outbox Pattern)
**O que fazer:**
- Implementar repositório Postgres usando `pgx`.
- Em uma única transação:
  1. `UPDATE orders SET status = ... WHERE order_id = ...`
  2. `INSERT INTO outbox (event_type, payload, status) VALUES (...)`
- Garantir que a falha em qualquer passo resulte em `ROLLBACK`.

**Critérios de Aceite:**
- [x] Atualização do pedido e inserção no outbox são atômicas.
- [x] Reprocessamento do Kafka não gera duplicidade no banco (Idempotência SQL via `last_seq_processed`).

### Task 3.5: Implementar DLQ (Dead Letter Queue)
**O que fazer:**
- Definir tópico Kafka `orders-dlq`.
- Implementar lógica de encaminhamento para DLQ em caso de:
  - Eventos órfãos (expiração no buffer).
  - Erros fatais de parsing/validação.
  - Excesso de retries no processamento.

**Critérios de Aceite:**
- [x] Mensagens problemáticas chegam à DLQ com headers de erro explicando a causa.

### Task 3.6: Handler de Tombstones (ABORT_PLAN)
**O que fazer:**
- Ao receber `ABORT_PLAN`, o servidor deve:
  1. Invalidar o `plan_id` no Redis (setar flag de cancelado).
  2. Limpar qualquer evento no Waiting Room associado ao plano.
  3. Marcar o pedido como `aborted` no Postgres.

**Critérios de Aceite:**
- [x] Recebimento de `ABORT_PLAN` interrompe processamento futuro do plano.
- [x] Redis é limpo imediatamente após o abort.

### Task 3.7: Webhook Dispatcher Async
**O que fazer:**
- Criar um componente Dispatcher que lê da tabela `outbox` e envia notificações HTTP.
- Implementar retry com backoff para falhas no Webhook.
- Marcar evento no outbox como `sent` ou `failed` após a tentativa.

**Critérios de Aceite:**
- [x] Notificações são enviadas de forma assíncrona sem bloquear o consumer Kafka.
- [x] Logs mostram tentativas de retry para webhooks fora do ar.
- [x] URL do Webhook configurável via variável de ambiente (Ajustado via Code Review).

### Task 3.8: Registro no Consul e Health Checks
**O que fazer:**
- Integrar SDK do Consul no startup do servidor Go.
- Registrar o serviço `nexus-server` e expor um endpoint `/health`.
- Configurar check de TTL no Consul para detectar quedas do servidor.

**Critérios de Aceite:**
- [x] Servidor aparece como `healthy` na UI do Consul.
- [x] Queda do servidor reflete no status do Consul em < 30s.

---

## Estrutura de Pastas Implementada
```
server/
  cmd/
    server/
      main.go
  internal/
    config/
    consumer/
    db/
    redis/
    dispatcher/
    telemetry/
  pkg/
    models/
  scripts/
    lua/
      check_and_set_seq.lua
```

---

## Histórico de Revisão (AI Code Review)
- **Data:** 2026-03-01
- **Melhorias Aplicadas:**
  - Tornou `WebhookURL` configurável via env no `config.go` e `dispatcher.go`.
  - Corrigiu inicialização de variáveis de ambiente no startup.
  - Identificou gaps de testes automatizados e worker de expiração de buffer.
