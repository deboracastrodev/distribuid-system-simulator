# ✅ Tasks de Desenvolvimento

Lista de tarefas operacionais para a execução do Nexus Event Gateway.
Decisoes tecnologicas baseadas nos ADRs registrados em `docs/blueprint-arquitetura.md` (Secao 6).

---

## 🟢 Fase 1: Infraestrutura e Contrato

- [x] **Task 1.1:** Criar `docker-compose.yml` com Kafka (KRaft), Redis Cluster e Postgres.
- [x] **Task 1.2:** Adicionar Consul ao `docker-compose.yml` para Service Discovery e Health Checks (ADR-002).
- [x] **Task 1.3:** Validar conectividade entre todos os containers (Kafka, Redis, Postgres, Consul).
- [x] **Task 1.4:** Definir JSON Schema para os eventos (`OrderCreated`, `InventoryValidated`, `ABORT_PLAN`, etc).
- [x] **Task 1.5:** Criar Script SQL de inicializacao (Postgres) com tabela de Pedidos e Outbox.

## 🔵 Fase 2: O Cerebro Agentico (Python)

- [x] **Task 2.1:** Configurar ambiente Python (venv/poetry) com dependencias LangGraph e kafka-python.
- [x] **Task 2.2:** Criar Agent Planner com LangGraph para gerar sequencias de eventos com `plan_id` e `seq_id` unicos.
- [x] **Task 2.3:** Integrar Producer Kafka no Python com ordering key (`order_id`) e injecao de headers (`traceparent`).
- [x] **Task 2.4:** Implementar evento `ABORT_PLAN` (Tombstone) para invalidacao de planos no Redis. *(Invalidacao movida para o Postgres na Task 7.3.)*

## 🔴 Fase 3: O Coracao de Go

- [x] **Task 3.1:** Inicializar modulo Go e configurar consumidor Kafka com **franz-go** (ADR-001).
- [x] **Task 3.2:** Desenvolver Script Lua (`check_and_set_seq.lua`) no Redis para validacao de sequencia atomica, com testes isolados do script. *(Substituido na Task 7.1 — ADR-004.)*
- [x] **Task 3.3:** Implementar logica de Waiting Room (buffer) no Redis para eventos fora de ordem, com TTL de 1h e encaminhamento para DLQ na expiracao. *(Buffer movido para `pending_events` no Postgres na Task 7.1.)*
- [x] **Task 3.4:** Implementar transacao ACID no Postgres (Update Order + Insert Outbox) em boundary transacional unico.
- [x] **Task 3.5:** Implementar DLQ (Dead Letter Queue) para eventos orfaos e falhas de processamento (RF04).
- [x] **Task 3.6:** Implementar handler de Tombstones — processar `ABORT_PLAN` para invalidar `plan_id` no Redis e limpar buffers. *(Refeito na Task 7.3.)*
- [x] **Task 3.7:** Implementar Webhook/Dispatcher async para notificacao de clientes apos processamento.
- [x] **Task 3.8:** Registrar servico Go no Consul e configurar health checks.

## 🟡 Fase 4: Coordenacao e Resiliencia

- [x] **Task 4.1:** Configurar Service Discovery via Consul DNS para comunicacao entre servicos.
- [x] **Task 4.2:** Implementar Circuit Breaker com configuracoes armazenadas no Consul KV.

## 🟣 Fase 5: Observabilidade e Prova de Conceito

- [x] **Task 5.1:** Adicionar OpenTelemetry ao Agent Python — injetar header `traceparent` em cada evento produzido. Adicionado decorator `@_traced_node` nos nodes LangGraph e `TraceIDFilter` nos logs.
- [x] **Task 5.2:** Adicionar OpenTelemetry ao Server Go — propagar `traceparent` em cada hop (Kafka -> Redis -> Postgres -> Webhook). Child spans adicionados: `redis.check-and-set-seq`, `redis.abort-plan`, `postgres.process-event`, `redis.drain-buffer`, etc. *(Apos a Task 7.1 os spans de sequencia sao `postgres.apply-event` e `postgres.abort-plan`.)*
- [x] **Task 5.3:** Subir Grafana + Jaeger no Docker Compose para visualizacao de traces distribuidos. Dashboard provisionado com data links para Jaeger UI e Explore.
- [x] **Task 5.4:** Criar script de Chaos Test cobrindo: sequence gaps, Redis restart e zombie events (ABORT_PLAN). Script em `scripts/chaos_test.py` com 3 cenarios validados.

## 🟠 Fase 6: Qualidade, Documentacao e Demo

- [x] **Task 6.1:** Criar testes unitarios para o Script Lua (`check_and_set_seq.lua`) — validar sequencia correta, duplicata, fora de ordem. *(O script foi substituido por `advance_seq.lua`, testado com miniredis em `server/internal/redis`; as regras de sequencia sao testadas por tabela em `server/internal/sequencing`.)*
- [ ] **Task 6.2:** Criar testes de integracao para o Consumer Go (franz-go + Redis + Postgres em containers de teste). *(Parcial: processamento e retry cobertos contra Postgres real + miniredis em `server/internal/consumer`. Falta cobrir o loop franz-go (poll/commit) com Kafka real.)*
- [ ] **Task 6.3:** Criar testes para o Agent Planner Python (geracao de `plan_id`/`seq_id`, producao de eventos, `ABORT_PLAN`).
- [ ] **Task 6.4:** Criar demo end-to-end — script que executa o fluxo completo: Agent -> Kafka -> Go -> Redis -> Postgres -> Webhook, provando exactly-once em acao.
- [ ] **Task 6.5:** Criar `README.md` com: diagrama de arquitetura, stack justificada (referenciando ADRs), instrucoes de setup (`docker-compose up`), e como rodar a demo E2E.

## ⚫ Fase 7: Correcao das Garantias (ADR-004)

- [x] **Task 7.1:** Mover a decisao de sequencia para uma transacao no Postgres (advisory lock por `order_id`), com o buffer de reordenacao em `pending_events` drenado na mesma transacao.
- [x] **Task 7.2:** Commitar offsets Kafka so para registros resolvidos: retry no lugar para erro transitorio, DLQ com ack do broker para erro permanente.
- [x] **Task 7.3:** `ABORT_PLAN` idempotente no Postgres: uma notificacao, tombstone para abort antes de qualquer evento, pedido `completed` terminal.
- [x] **Task 7.4:** Rebaixar o Redis a cache monotonico (`advance_seq.lua`), escrito apos o commit; `/health` independente do Redis.
- [x] **Task 7.5:** Ordem deterministica do outbox (`outbox.position`), para eventos drenados na mesma transacao.
- [x] **Task 7.6:** Testes de integracao contra Postgres real: gap, reordenacao, abort, entregas concorrentes, expiracao do buffer, falha transitoria do banco, perda do Redis.
- [x] **Task 7.7:** Remover a Fase 5 (reenvio de todos os eventos) do cenario Redis Restart em `scripts/chaos_test.py` e exigir que 100% dos pedidos terminem `completed` com seq 5, com exatamente uma notificacao por evento no outbox. Contra o server anterior ao ADR-004 o cenario falha (0/10 pedidos completos).
- [x] **Task 7.8:** Criar o topico `orders-dlq` sob demanda no producer da DLQ (franz-go nao cria topicos ao produzir) e adicionar o cenario de chaos "Poison Messages": JSON malformado e eventos invalidos intercalados com planos validos devem ir para a DLQ sem travar a particao.

## ⚪ Fase 8: Integracao Continua

- [x] **Task 8.1:** Workflow `.github/workflows/ci.yml` em todo push: Go (`gofmt`, `go mod tidy` sem diff, `go vet`, `go test -race` com Postgres 16 como service container e `REQUIRE_INTEGRATION=1`, que faz um teste de integracao sem banco falhar em vez de ser pulado), Agent (`pytest`) e E2E + Chaos com `docker compose` (e2e com 50 planos e os 4 cenarios de chaos).
- [x] **Task 8.2:** `.gitignore` passa a versionar `.github/workflows/` (o resto de `.github/` segue ignorado por guardar arquivos locais de ferramentas de agente).
- [x] **Task 8.3:** `make up` e `make wait` reconstroem imagens alteradas (`--build`); antes, subiam a imagem antiga do server depois de um `git pull`.
- [x] **Task 8.4:** Higiene exigida pelo CI: `gofmt` no dispatcher e remocao de artefatos versionados (`agent/nexus_agent.egg-info/`, `package-lock.json` vazio).

## 🟤 Fase 9: Entrega de Webhooks (ADR-005)

- [x] **Task 9.1:** Receptor de webhooks para testes (`scripts/webhook_sink.py`, servico `webhook-sink` no compose): registra entregas, verifica ordem por `seq_id`, duplicatas e `Idempotency-Key`, e injeta 503/400.
- [x] **Task 9.2:** Cenario de chaos "Webhook Delivery" (503 a cada 3 requisicoes e um pedido rejeitado com 400). Falha na versao anterior do dispatcher: sem `Idempotency-Key`, 4xx retentado e ordem `[2, 3, 4, 5, 1]`.
- [x] **Task 9.3:** Dispatcher reescrito: so a cabeca de cada pedido e elegivel (ordem por pedido), `FOR UPDATE SKIP LOCKED` + lease, retry agendado no banco com backoff, 4xx em dead-letter imediato, `Idempotency-Key`, entrega paralela entre pedidos (`WEBHOOK_WORKERS`, antes ignorado).
- [x] **Task 9.4:** Testes de integracao do dispatcher contra Postgres real: ordem, isolamento entre pedidos, dead-letter por 4xx e por tentativas, lease expirado, circuito aberto, shutdown e duas instancias concorrentes.

## 🔘 Fase 10: Metricas (ADR-006)

- [x] **Task 10.1:** Pacote `internal/metrics` com `prometheus/client_golang` e registry injetado; `/metrics` na porta 8080.
- [x] **Task 10.2:** Consumer: desfechos do sequenciamento, eventos drenados, DLQ por codigo (so apos ack), retries, tempo ate resolver, consumer lag e falhas do cache.
- [x] **Task 10.3:** Dispatcher: resultado das entregas, latencia do webhook e estado do circuit breaker. Backlog do outbox e do buffer lido do Postgres no scrape.
- [x] **Task 10.4:** Prometheus 3.13 no compose, datasource e dashboard "Nexus Event Gateway - Metricas" provisionados no Grafana.
- [x] **Task 10.5:** `scripts/check_metrics.py` no CI: metricas coerentes com o e2e e o chaos, scrape do Prometheus, todas as queries do dashboard com dados, e dashboard e datasource provisionados no Grafana.

## 🔶 Fase 11: Agent com Falhas Simuladas

- [x] **Task 11.1:** Falhas de estoque e de pagamento com taxas configuraveis (`--inventory-failure-rate`, `--payment-rejection-rate`, env `SIM_*`). A decisao e um hash de (seed, passo), sem RNG no state; cada plano de uma execucao recebe a seed `<seed>:<posicao>`, entao `--seed` reproduz a execucao inteira. Os codigos `inventory_failed` e `payment_rejected`, antes nunca emitidos, passam a ser exercitados.
- [x] **Task 11.2:** O grafo desvia para `abort_plan` depois de estoque ou pagamento; um pagamento recusado nao gera `PaymentProcessed`.
- [x] **Task 11.3:** Publicacao passo a passo (`graph.stream`) com atraso opcional entre eventos (`--step-delay-ms`) e ack do broker por evento: falha de entrega interrompe o agent com codigo 1 em vez de ser so logada.
- [x] **Task 11.4:** Gerador de carga (`--orders N`) com relatorio JSON por plano (`--report`).
- [x] **Task 11.5:** `scripts/check_agent_outcomes.py` no CI: 40 planos do agent (imagem do compose) conferidos no Postgres, no outbox e no webhook sink.
- [x] **Task 11.6:** Logs do agent: o filtro que injeta `trace_id` estava no logger raiz, que nao filtra records de loggers filhos; toda linha de log falhava com `KeyError: 'otelTraceID'`. O filtro passou para o handler.

## 🧭 Fase 12: Tese e Modelo de Falhas (ADR-007, ADR-008)

- [x] **Task 12.1:** ADR-007: o projeto passa a ser um gateway de execucao segura de planos de agentes de IA. Catalogo de falhas de agente (F1–F14) com o estado atual de cada uma, o que o gateway garante e o que nao garante, e o roteiro das fases 13 a 17.
- [x] **Task 12.2:** Corrigir o termo *exactly-once* no README e no blueprint: aplicado uma vez no Postgres (*effectively-once*), entregue pelo menos uma vez ao mundo externo, com `Idempotency-Key`.
- [x] **Task 12.3:** Remover o Redis (ADR-008): consumer consulta so o Postgres; saem `internal/redis`, `advance_seq.lua`, a metrica `nexus_seq_cache_errors_total` e os desfechos `cache_*`.
- [x] **Task 12.4:** Remover o Consul (ADR-008): circuit breaker configurado por variaveis de ambiente validadas no startup (`WEBHOOK_CB_*`, `WEBHOOK_TIMEOUT`); servicos pelo nome do compose.
- [x] **Task 12.5:** Cenario de chaos "Server Crash" no lugar de "Redis Restart": com um advisory lock segurando o ultimo evento do lote, o server e morto com eventos aplicados e sem commit no Kafka. O cenario exige a reentrega (eventos ignorados como duplicados) e nenhuma notificacao duplicada; com a deduplicacao desligada, acusa 20 duplicatas.
- [x] **Task 12.6:** `scripts/check_metrics.py` confere os contadores pelo Prometheus (`increase()`), que trata o reinicio do server no cenario de crash.

## 🛡️ Fase 13: O Gateway Nao Confia no Agente (planejada)

- [ ] **Task 13.1:** Definicao do processo no server: evento valido para cada estado; `completed` e `aborted` sao finais (F7, F8).
- [ ] **Task 13.2:** Checagens de valor no server: itens nao vazios, total dentro do limite, pagamento igual ao total (F9).
- [ ] **Task 13.3:** Violacao aborta o pedido com `policy_violation` e notifica; metrica de acoes bloqueadas por motivo.
- [ ] **Task 13.4:** Agente adversarial no CI: comete cada violacao; nenhuma chega ao webhook.

## ⏳ Fase 14: Ciclo de Vida do Plano (planejada)

- [ ] **Task 14.1:** `GET /orders/{id}` com status, `plan_id` e `last_seq`, para o agent retomar em vez de recomecar (F10, F12).
- [ ] **Task 14.2:** Troca explicita de plano: o plano novo substitui o antigo; eventos atrasados do antigo sao descartados (F10).
- [ ] **Task 14.3:** Prazo do plano: pedido parado alem do prazo vira `failed` e notifica (F11).

## 🔁 Fase 15: Efeitos Executados pelo Gateway (planejada)

- [ ] **Task 15.1:** Servicos simulados de estoque e pagamento chamados pelo outbox com `Idempotency-Key`; resultados publicados em `order-results`. As taxas de falha saem do agent (F13).
- [ ] **Task 15.2:** Agente reativo: espera o resultado de cada passo; compensa (estorno) quando uma falha acontece depois do pagamento.

## 🤖 Fase 16: Planner com LLM (planejada)

- [ ] **Task 16.1:** Planner com LLM via OpenRouter (tool use com os passos do processo), opcional; o deterministico continua o padrao do CI.
- [ ] **Task 16.2:** Relatorio de acoes propostas, aplicadas e bloqueadas por motivo, incluindo um cenario com instrucao adversarial; job noturno quando a chave estiver configurada (F14).

## 📏 Fase 17: Numeros e Narrativa (planejada)

- [ ] **Task 17.1:** Benchmark: vazao, latencia p50/p99 por passo, custo do lock em pedido disputado.
- [ ] **Task 17.2:** Painel de acoes bloqueadas no Grafana, `make demo` e texto final (problema, garantias, prova, custo, limites).
