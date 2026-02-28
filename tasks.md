# ✅ Tasks de Desenvolvimento

Lista de tarefas operacionais para a execução do Nexus Event Gateway.

---

## 🟢 Fase 1: Infraestrutura e Contrato
- [x] **Task 1.1:** Criar `docker-compose.yml` com Kafka (KRaft), Redis Cluster e Postgres.
- [ ] **Task 1.2:** Validar conectividade entre containers.
- [ ] **Task 1.3:** Definir JSON Schema para os eventos (`OrderCreated`, `InventoryValidated`, etc).
- [ ] **Task 1.4:** Criar Script SQL de inicialização (Postgres) com tabela de Pedidos e Outbox.

## 🔵 Fase 2: O Cérebro Agêntico (Python)
- [ ] **Task 2.1:** Configurar ambiente Python (venv/poetry).
- [ ] **Task 2.2:** Criar Agent Planner com LangGraph para gerar sequências de eventos.
- [ ] **Task 2.3:** Integrar Producer Kafka no Python com injeção de headers.

## 🔴 Fase 3: O Coração de Go
- [ ] **Task 3.1:** Inicializar módulo Go e configurar consumidor Kafka (confluent-kafka-go ou sarama).
- [ ] **Task 3.2:** Desenvolver Script Lua no Redis para validação de sequência atômica.
- [ ] **Task 3.3:** Implementar lógica de buffer no Redis para eventos fora de ordem.
- [ ] **Task 3.4:** Implementar transação ACID no Postgres (Update Order + Insert Outbox).

## 🟣 Fase 4: Observabilidade e Prova de Conceito
- [ ] **Task 4.1:** Adicionar suporte a OpenTelemetry (Headers Propagation).
- [ ] **Task 4.2:** Subir Grafana/Jaeger para visualização de Traces.
- [ ] **Task 4.3:** Criar script de "Chaos Test" (simular quedas de rede e reprocessamento).
