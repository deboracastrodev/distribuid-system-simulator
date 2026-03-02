.PHONY: help up down down-clean restart status logs validate wait agent-build agent-run agent-dry-run agent-abort-test

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

# --- Server Go ---

server-build: ## Builda a imagem do server Go
	@docker compose build server

server-up: ## Sobe o server Go (e dependências)
	@docker compose up -d server

server-logs: ## Segue logs do server Go
	@docker compose logs -f server

server-restart: ## Reinicia o server Go
	@docker compose restart server
