.PHONY: help up down down-clean restart status logs validate wait agent-build agent-run agent-dry-run agent-simulate agent-check agent-e2e chaos-test chaos-test-gaps chaos-test-crash chaos-test-zombie chaos-test-poison chaos-test-webhook webhook-stats metrics metrics-check prometheus-open grafana-open jaeger-open

help: ## Exibe esta ajuda
	@grep -E '^[a-zA-Z_%-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2}'

up: ## Sobe todos os containers em background (reconstrói imagens alteradas)
	@docker compose up -d --build

down: ## Derruba todos os containers
	@docker compose down

down-clean: ## Derruba containers e remove volumes
	@docker compose down -v

restart: down up ## Reinicia todos os containers

status: ## Mostra status dos containers
	@docker compose ps

logs: ## Segue logs de todos os containers
	@docker compose logs -f

logs-%: ## Segue logs de um serviço (ex: make logs-kafka, make logs-server)
	@docker compose logs -f $*

validate: ## Valida conectividade com todos os serviços
	@bash scripts/validate-connectivity.sh

wait: ## Sobe containers, espera health checks e valida
	@docker compose up -d --build --wait
	@bash scripts/validate-connectivity.sh

# --- Agent Planner ---

agent-build: ## Builda a imagem do agent
	@docker compose build agent

agent-run: ## Executa o agent e publica eventos no Kafka (pedido exemplo)
	@docker compose run --rm agent python -m src.main

agent-dry-run: ## Gera eventos sem publicar no Kafka
	@docker compose run --rm agent python -m src.main --dry-run

agent-order: ## Publica pedido custom (ex: make agent-order ORDER='{"user_id":"u1","items":[...],"total_amount":10}')
	@docker compose run --rm agent python -m src.main --order '$(ORDER)'

agent-simulate: ## Gera planos com falhas simuladas (ex: make agent-simulate ORDERS=50 INV_RATE=0.3 PAY_RATE=0.2 SEED=demo DELAY_MS=200)
	@docker compose run --rm --user "$$(id -u):$$(id -g)" -v "$(CURDIR):/out" agent python -m src.main \
		--orders $(or $(ORDERS),20) \
		--inventory-failure-rate $(or $(INV_RATE),0.2) \
		--payment-rejection-rate $(or $(PAY_RATE),0.25) \
		--step-delay-ms $(or $(DELAY_MS),0) \
		$(if $(SEED),--seed $(SEED)) \
		--report /out/agent-report.json

agent-check: ## Confere no Postgres e no webhook sink o desfecho de cada plano de agent-report.json
	@$(CHAOS_PYTHON) scripts/check_agent_outcomes.py agent-report.json

agent-e2e: agent-simulate agent-check ## agent-simulate + agent-check (requer infra up e chaos-deps)

agent-test: ## Roda testes unitários do Agent Python via Docker
	@docker compose run --rm --no-deps agent python -m pytest tests/ -v

agent-studio: ## Abre LangGraph Studio (API local + docs em http://127.0.0.1:2024/docs)
	@cd agent && .venv/bin/langgraph dev

# --- Server Go ---

server-build: ## Builda a imagem do server Go
	@docker compose build server

server-up: ## Sobe o server Go (e dependências)
	@docker compose up -d server

server-logs: ## Segue logs do server Go
	@docker compose logs -f server

server-restart: ## Reinicia o server Go
	@docker compose restart server

server-test: ## Roda testes unitários do Go via Docker (sem infra; integração é pulada)
	@docker run --rm -v $(PWD)/server:/app -w /app golang:1.22-alpine sh -c "go mod tidy && go test ./... -v"

server-test-integration: ## Roda todos os testes Go contra o Postgres do compose (requer infra up; usa schema isolado)
	@docker run --rm --network distribuid-system-simulator_nexus -v $(PWD):/repo -w /repo/server \
		-e POSTGRES_DSN="postgres://nexus_user:nexus_pass@nexus-postgres:5432/nexus_db?sslmode=disable" \
		golang:1.22-alpine sh -c "go mod tidy && go test ./... -v -count=1"

server-lint: ## Roda o linter (golangci-lint se disponível)
	@cd server && golangci-lint run ./... || echo "Linter not installed"

# --- Debug & Monitoring ---

db-cli: ## Acessa o Postgres local
	@docker compose exec postgres psql -U nexus_user -d nexus_db

kafka-topics: ## Lista tópicos Kafka
	@docker compose exec kafka /opt/kafka/bin/kafka-topics.sh --list --bootstrap-server localhost:29092

kafka-consume: ## Consome eventos do tópico orders (debug manual)
	@docker compose exec kafka /opt/kafka/bin/kafka-console-consumer.sh --bootstrap-server localhost:29092 --topic orders --from-beginning

kafka-dlq: ## Consome eventos da DLQ
	@docker compose exec kafka /opt/kafka/bin/kafka-console-consumer.sh --bootstrap-server localhost:29092 --topic orders-dlq --from-beginning

# --- Validação E2E (Fluxo Completo) ---

demo-full: up ## Sobe infra e executa agent para gerar fluxo completo
	@echo "Aguardando infra... (5s)"
	@sleep 5
	@$(MAKE) agent-run
	@echo "Fluxo gerado! Verifique logs com 'make server-logs' e banco com 'make db-check'"

demo-e2e: ## Roda demo E2E: envia N planos e valida que cada evento foi aplicado e notificado uma vez (requer infra up)
	@$(CHAOS_PYTHON) scripts/e2e_demo.py --plans $(or $(PLANS),10)

db-check: ## Mostra estado atual dos pedidos no banco
	@docker compose exec postgres psql -U nexus_user -d nexus_db -c "SELECT id, status, last_seq_processed, updated_at FROM orders ORDER BY updated_at DESC LIMIT 5;"
	@docker compose exec postgres psql -U nexus_user -d nexus_db -c "SELECT id, event_type, processed, created_at FROM outbox ORDER BY created_at DESC LIMIT 5;"

# --- Observabilidade (Fase 5) ---

grafana-open: ## Abre Grafana no navegador (localhost:3000, admin/nexus)
	@open http://localhost:3000 2>/dev/null || xdg-open http://localhost:3000 2>/dev/null || echo "Acesse http://localhost:3000 (admin/nexus)"

jaeger-open: ## Abre Jaeger UI no navegador (localhost:16686)
	@open http://localhost:16686 2>/dev/null || xdg-open http://localhost:16686 2>/dev/null || echo "Acesse http://localhost:16686"

# --- Chaos Test (Fase 5) ---

CHAOS_PYTHON = .venv/bin/python3

chaos-deps: ## Instala dependências do chaos test no venv local
	@test -d .venv || python3 -m venv .venv
	@.venv/bin/pip install -q docker psycopg2-binary confluent-kafka

chaos-test: ## Roda todos os cenários de chaos test
	@$(CHAOS_PYTHON) scripts/chaos_test.py --orders 10

chaos-test-gaps: ## Roda apenas cenário de Sequence Gaps
	@$(CHAOS_PYTHON) scripts/chaos_test.py --scenario gaps --orders 10

chaos-test-crash: ## Roda apenas cenário de crash do server (SIGKILL no meio dos planos)
	@$(CHAOS_PYTHON) scripts/chaos_test.py --scenario crash --orders 10

chaos-test-zombie: ## Roda apenas cenário de Zombie Events
	@$(CHAOS_PYTHON) scripts/chaos_test.py --scenario zombie --orders 10

chaos-test-poison: ## Roda apenas cenário de mensagens envenenadas (DLQ sem travar a partição)
	@$(CHAOS_PYTHON) scripts/chaos_test.py --scenario poison --orders 10

chaos-test-webhook: ## Roda apenas cenário de entrega de webhooks com receptor instável
	@$(CHAOS_PYTHON) scripts/chaos_test.py --scenario webhook --orders 10

webhook-stats: ## Mostra as entregas recebidas pelo webhook sink, por pedido
	@curl -s http://localhost:9090/stats | python3 -m json.tool

# --- Métricas (Prometheus) ---

metrics: ## Mostra as métricas nexus_* expostas pelo server
	@curl -s http://localhost:8080/metrics | grep -E '^nexus_'

metrics-check: ## Valida server -> Prometheus -> dashboard (rodar depois de demo-e2e e chaos-test)
	@python3 scripts/check_metrics.py

prometheus-open: ## Abre o Prometheus no navegador (localhost:9095)
	@open http://localhost:9095 2>/dev/null || xdg-open http://localhost:9095 2>/dev/null || echo "Acesse http://localhost:9095"
