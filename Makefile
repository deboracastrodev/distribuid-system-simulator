.PHONY: help up down down-clean restart status logs validate wait

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
