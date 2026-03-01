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

- [ ] **Task 2.1:** Configurar ambiente Python (venv/poetry) com dependencias LangGraph e kafka-python.
- [ ] **Task 2.2:** Criar Agent Planner com LangGraph para gerar sequencias de eventos com `plan_id` e `seq_id` unicos.
- [ ] **Task 2.3:** Integrar Producer Kafka no Python com ordering key (`order_id`) e injecao de headers (`traceparent`).
- [ ] **Task 2.4:** Implementar evento `ABORT_PLAN` (Tombstone) para invalidacao de planos no Redis.

## 🔴 Fase 3: O Coracao de Go

- [ ] **Task 3.1:** Inicializar modulo Go e configurar consumidor Kafka com **franz-go** (ADR-001).
- [ ] **Task 3.2:** Desenvolver Script Lua (`check_and_set_seq.lua`) no Redis para validacao de sequencia atomica, com testes isolados do script.
- [ ] **Task 3.3:** Implementar logica de Waiting Room (buffer) no Redis para eventos fora de ordem, com TTL de 1h e encaminhamento para DLQ na expiracao.
- [ ] **Task 3.4:** Implementar transacao ACID no Postgres (Update Order + Insert Outbox) em boundary transacional unico.
- [ ] **Task 3.5:** Implementar DLQ (Dead Letter Queue) para eventos orfaos e falhas de processamento (RF04).
- [ ] **Task 3.6:** Implementar handler de Tombstones — processar `ABORT_PLAN` para invalidar `plan_id` no Redis e limpar buffers.
- [ ] **Task 3.7:** Implementar Webhook/Dispatcher async para notificacao de clientes apos processamento.
- [ ] **Task 3.8:** Registrar servico Go no Consul e configurar health checks.

## 🟡 Fase 4: Coordenacao e Resiliencia

- [ ] **Task 4.1:** Configurar Service Discovery via Consul DNS para comunicacao entre servicos.
- [ ] **Task 4.2:** Implementar Circuit Breaker com configuracoes armazenadas no Consul KV.

## 🟣 Fase 5: Observabilidade e Prova de Conceito

- [ ] **Task 5.1:** Adicionar OpenTelemetry ao Agent Python — injetar header `traceparent` em cada evento produzido.
- [ ] **Task 5.2:** Adicionar OpenTelemetry ao Server Go — propagar `traceparent` em cada hop (Kafka -> Redis -> Postgres -> Webhook).
- [ ] **Task 5.3:** Subir Grafana + Jaeger no Docker Compose para visualizacao de traces distribuidos.
- [ ] **Task 5.4:** Criar script de Chaos Test cobrindo: queda de rede, reprocessamento Kafka, eventos fora de ordem, expiracao de TTL no buffer, e ABORT_PLAN.

## 🟠 Fase 6: Qualidade, Documentacao e Demo

- [ ] **Task 6.1:** Criar testes unitarios para o Script Lua (`check_and_set_seq.lua`) — validar sequencia correta, duplicata, fora de ordem.
- [ ] **Task 6.2:** Criar testes de integracao para o Consumer Go (franz-go + Redis + Postgres em containers de teste).
- [ ] **Task 6.3:** Criar testes para o Agent Planner Python (geracao de `plan_id`/`seq_id`, producao de eventos, `ABORT_PLAN`).
- [ ] **Task 6.4:** Criar demo end-to-end — script que executa o fluxo completo: Agent -> Kafka -> Go -> Redis -> Postgres -> Webhook, provando exactly-once em acao.
- [ ] **Task 6.5:** Criar `README.md` com: diagrama de arquitetura, stack justificada (referenciando ADRs), instrucoes de setup (`docker-compose up`), e como rodar a demo E2E.
