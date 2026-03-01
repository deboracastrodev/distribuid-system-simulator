#!/usr/bin/env bash
# =============================================================================
# Nexus Event Gateway — Validação de Conectividade
# Testa conectividade host→container e inter-container para todos os serviços
# =============================================================================

set -euo pipefail

# Cores
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[0;33m'
CYAN='\033[0;36m'
BOLD='\033[1m'
NC='\033[0m'

# Contadores
PASS=0
FAIL=0

print_header() {
  echo ""
  echo -e "${BOLD}${CYAN}=============================================${NC}"
  echo -e "${BOLD}${CYAN}  Nexus Event Gateway — Conectividade${NC}"
  echo -e "${BOLD}${CYAN}  $(date '+%Y-%m-%d %H:%M:%S')${NC}"
  echo -e "${BOLD}${CYAN}=============================================${NC}"
  echo ""
}

check() {
  local label="$1"
  shift
  local output
  if output=$("$@" 2>&1); then
    echo -e "  ${GREEN}✔ ${label}${NC}"
    PASS=$((PASS + 1))
  else
    echo -e "  ${RED}✘ ${label}${NC}"
    if [ -n "$output" ]; then
      echo -e "    ${YELLOW}→ ${output}${NC}"
    fi
    FAIL=$((FAIL + 1))
  fi
}

# Verificar Docker Compose v2
if ! docker compose version >/dev/null 2>&1; then
  echo -e "${RED}ERRO: Docker Compose v2 não encontrado.${NC}"
  echo "Este projeto requer 'docker compose' (v2, com espaço), não 'docker-compose' (v1, com hífen)."
  exit 1
fi

print_header

# ─── Fase 1: Host → Container ───────────────────────────────────────────────
echo -e "${BOLD}Fase 1: Validação host → container${NC}"
echo ""

check "Postgres (pg_isready)" \
  docker compose exec -T postgres pg_isready -U nexus_user -d nexus_db

check "Redis (ping com auth)" \
  docker compose exec -T redis redis-cli -a nexus_pass ping

check "Kafka (listar tópicos)" \
  docker compose exec -T kafka /opt/kafka/bin/kafka-topics.sh --bootstrap-server localhost:29092 --list

check "Consul (members)" \
  docker compose exec -T consul consul members

check "Jaeger (UI HTTP)" \
  docker compose exec -T jaeger wget -q -O /dev/null http://localhost:16686

PHASE1_PASS=$PASS
PHASE1_FAIL=$FAIL

# ─── Fase 2: Inter-container ────────────────────────────────────────────────
echo ""
echo -e "${BOLD}Fase 2: Validação inter-container (via Postgres)${NC}"
echo ""

# Resetar contadores para fase 2
# Usa o container postgres (Debian + bash) que suporta /dev/tcp
PASS=0
FAIL=0

check "Postgres → Redis (porta 6379)" \
  docker compose exec -T postgres bash -c "echo > /dev/tcp/redis/6379"

check "Postgres → Consul (porta 8500)" \
  docker compose exec -T postgres bash -c "echo > /dev/tcp/consul/8500"

check "Postgres → Kafka (porta 29092 INTERNAL)" \
  docker compose exec -T postgres bash -c "echo > /dev/tcp/kafka/29092"

check "Postgres → Jaeger (porta 16686)" \
  docker compose exec -T postgres bash -c "echo > /dev/tcp/jaeger/16686"

PHASE2_PASS=$PASS
PHASE2_FAIL=$FAIL

# ─── Resumo ─────────────────────────────────────────────────────────────────
TOTAL_PASS=$((PHASE1_PASS + PHASE2_PASS))
TOTAL_FAIL=$((PHASE1_FAIL + PHASE2_FAIL))

echo ""
echo -e "${BOLD}${CYAN}─────────────────────────────────────────────${NC}"
echo -e "${BOLD}  Resumo${NC}"
echo -e "  Fase 1 (host→container):  ${GREEN}${PHASE1_PASS} OK${NC}  ${RED}${PHASE1_FAIL} FAIL${NC}"
echo -e "  Fase 2 (inter-container): ${GREEN}${PHASE2_PASS} OK${NC}  ${RED}${PHASE2_FAIL} FAIL${NC}"
echo -e "  Total:                    ${GREEN}${TOTAL_PASS} OK${NC}  ${RED}${TOTAL_FAIL} FAIL${NC}"
echo -e "${BOLD}${CYAN}─────────────────────────────────────────────${NC}"
echo ""

if [ "$TOTAL_FAIL" -gt 0 ]; then
  echo -e "${RED}${BOLD}RESULTADO: FALHA${NC}"
  exit 1
else
  echo -e "${GREEN}${BOLD}RESULTADO: TODOS OS SERVIÇOS OK${NC}"
  exit 0
fi
