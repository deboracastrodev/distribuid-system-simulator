.PHONY: help up down down-clean restart status logs validate wait agent-build agent-run agent-dry-run agent-abort-test chaos-test chaos-test-gaps chaos-test-redis chaos-test-zombie grafana-open jaeger-open

help: ## Exibe esta ajuda
	@grep -E '^[a-zA-Z_%-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2}'

up: ## Sobe todos os containers em background
	@docker compose up -d

down: ## Derruba todos os containers
	@docker compose down

down-clean: ## Derruba containers e remove volumes
	@docker compose down -v

restart: down up ## Reinicia todos os containers

status: ## Mostra status dos containers
	@docker compose ps

logs: ## Segue logs de todos os containers
	@docker compose logs -f

logs-%: ## Segue logs de um serviço (ex: make logs-kafka, make logs-consul)
	@docker compose logs -f $*

validate: ## Valida conectividade com todos os serviços
	@bash scripts/validate-connectivity.sh

wait: ## Sobe containers, espera health checks e valida
	@docker compose up -d --wait
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

agent-test: ## Roda testes unitários do Agent Python via Docker
	@docker compose run --rm --no-deps agent python -m pytest tests/ -v

agent-studio: ## Abre LangGraph Studio (interface visual do grafo)
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

server-test: ## Roda testes unitários do Go (Lua + unit) via Docker
	@docker run --rm -v $(PWD)/server:/app -w /app golang:1.22-alpine sh -c "go mod tidy && go test ./scripts/lua/ -v"

server-test-all: ## Roda todos os testes Go via Docker
	@docker run --rm -v $(PWD)/server:/app -w /app golang:1.22-alpine sh -c "go mod tidy && go test ./... -v"

server-test-integration: ## Roda testes de integração do consumer (requer infra up)
	@docker run --rm --network distribuid-system-simulator_nexus -v $(PWD)/server:/app -w /app \
		-e REDIS_ADDR=nexus-redis:6379 -e REDIS_PASSWORD=nexus_pass \
		-e POSTGRES_DSN="postgres://nexus_user:nexus_pass@nexus-postgres:5432/nexus_db?sslmode=disable" \
		golang:1.22-alpine sh -c "go mod tidy && go test ./internal/consumer/ -v -count=1"

server-lint: ## Roda o linter (golangci-lint se disponível)
	@cd server && golangci-lint run ./... || echo "Linter not installed"

# --- Debug & Monitoring ---

redis-cli: ## Acessa o Redis local
	@docker compose exec redis redis-cli -a nexus_pass

redis-monitor: ## Monitora comandos Redis em tempo real (veja o Script Lua rodando!)
	@docker compose exec redis redis-cli -a nexus_pass monitor

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

demo-e2e: ## Roda demo E2E: envia N planos e valida Exactly-Once (requer infra up)
	@$(CHAOS_PYTHON) scripts/e2e_demo.py --plans $(or $(PLANS),10)

db-check: ## Mostra estado atual dos pedidos no banco
	@docker compose exec postgres psql -U nexus_user -d nexus_db -c "SELECT id, status, last_seq_processed, updated_at FROM orders ORDER BY updated_at DESC LIMIT 5;"
	@docker compose exec postgres psql -U nexus_user -d nexus_db -c "SELECT id, event_type, processed, created_at FROM outbox ORDER BY created_at DESC LIMIT 5;"

# --- Consul KV (Circuit Breaker Config) ---

consul-kv-list: ## Lista todas as configs do Circuit Breaker no Consul KV
	@curl -s http://localhost:8500/v1/kv/nexus/config/cb/?recurse | python3 -m json.tool 2>/dev/null || echo "No keys found"

consul-kv-set: ## Altera config CB (ex: make consul-kv-set KEY=webhook_failure_threshold VAL=3)
	@curl -s -X PUT -d '$(VAL)' http://localhost:8500/v1/kv/nexus/config/cb/$(KEY) && echo " OK: $(KEY)=$(VAL)"

consul-kv-get: ## Lê config CB (ex: make consul-kv-get KEY=webhook_failure_threshold)
	@curl -s http://localhost:8500/v1/kv/nexus/config/cb/$(KEY)?raw && echo ""

consul-services: ## Lista serviços registrados no Consul
	@curl -s http://localhost:8500/v1/agent/services | python3 -m json.tool

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

chaos-test-redis: ## Roda apenas cenário de Redis Restart
	@$(CHAOS_PYTHON) scripts/chaos_test.py --scenario redis --orders 10

chaos-test-zombie: ## Roda apenas cenário de Zombie Events
	@$(CHAOS_PYTHON) scripts/chaos_test.py --scenario zombie --orders 10
