# 🚀 Tech Spec: Fase 4 - Coordenação e Resiliência (Nexus Event Gateway)

Este documento detalha a implementação do Service Discovery via Consul DNS e a introdução de Circuit Breaker gerenciado centralizadamente via Consul KV.

## 1. Objetivos (Scope)
- **SD-01:** Migrar a comunicação entre serviços para nomes de domínio Consul (`.service.consul`).
- **CB-01:** Implementar Circuit Breaker no Server Go para proteger chamadas a Webhooks externos.
- **CB-02:** Armazenar e ler configurações do Circuit Breaker no Consul KV (Thresholds, Timeouts).

## 2. Investigação Técnica (Findings)
- O Consul já está no `docker-compose.yml` no modo `-dev`.
- O Server Go já se registra no Consul com TTL health checks.
- Atualmente, os serviços usam nomes de rede do Docker (e.g., `kafka:29092`, `postgres:5432`).
- Não há Circuit Breaker implementado no dispatcher de webhooks.

## 3. Plano de Implementação

### 3.1. Service Discovery (Consul DNS)
- **Docker Compose:** Configurar `dns` e `dns_search` em todos os containers para apontar para o container `consul`.
- **Refatoração de Config:** Substituir hosts fixos nos arquivos `.env` ou variáveis de ambiente por nomes Consul (ex: `redis.service.consul`).

### 3.2. Circuit Breaker (Go Server)
- **Library:** Adicionar `github.com/sony/gobreaker`.
- **Implementação:**
    - Criar `internal/consul/kv.go` para ler configurações (`nexus/config/cb/webhook_failure_threshold`, etc).
    - Integrar o Circuit Breaker no `internal/dispatcher/dispatcher.go`.
    - Transição de estados: Closed (Normal), Open (Failing), Half-Open (Testing).

## 4. Stories de Implementação (BMAD Format)

### [QS-STORY-01] Configurar Consul DNS no Docker Compose
- **Contexto:** Para que `service.consul` funcione, o Docker precisa encaminhar queries DNS para o Consul.
- **Ações:**
    - Atualizar `docker-compose.yml` com blocos `dns` apontando para o IP do Consul na rede Docker.
    - Testar resolução de `nexus-server.service.consul` de dentro de outros containers.
- **Critérios de Aceite:** `ping nexus-server.service.consul` deve resolver de dentro do container do agente.

### [QS-STORY-02] Implementar Circuit Breaker no Dispatcher
- **Contexto:** Falhas em webhooks externos não devem causar contenção de recursos ou retentativas infinitas no Gateway.
- **Ações:**
    - Adicionar `gobreaker` ao `go.mod`.
    - Criar wrapper em `dispatcher.go` para chamadas HTTP.
    - Configurar CB usando `consulapi.KV().Get()`.
- **Critérios de Aceite:** Se o webhook falhar 5 vezes seguidas (threshold default), o CB deve abrir e as próximas chamadas devem falhar fast sem tentar o HTTP.

### [QS-STORY-03] Configuração Centralizada via Consul KV
- **Contexto:** Thresholds de resiliência devem ser ajustáveis sem restart de pod/container.
- **Ações:**
    - Criar script ou comando CLI para popular o KV do Consul no startup.
    - Implementar lógica no Go para recarregar configs (polling ou watch).
- **Critérios de Aceite:** Alterar um valor no Consul KV deve refletir no comportamento do CB em runtime.

## 5. Riscos e Mitigações
- **DNS Loop:** O DNS do Docker pode conflitar com o Consul se não for bem configurado. *Mitigação: Usar `--dns-search` explicitamente.*
- **Indisponibilidade do Consul:** Se o Consul cair, o SD falha. *Mitigação: Manter fallback para o DNS interno do Docker em caso de erro na resolução.*
---
*Status: Done (AI Reviewed & Fixed)*
---

## 6. Dev Agent Record (AI)
### 6.1. File List
- `docker-compose.yml`: Configuração de DNS e migração para `.service.consul`.
- `server/cmd/server/main.go`: Inicialização resiliente do Consul KV e registro de serviço.
- `server/internal/consul/kv.go`: Implementação do watcher de configurações do Consul.
- `server/internal/consul/kv_test.go`: Testes de unidade para robustez do watcher.
- `server/internal/dispatcher/dispatcher.go`: Implementação do Circuit Breaker dinâmico com interface para o repositório.
- `server/internal/dispatcher/dispatcher_test.go`: Testes de integração do Circuit Breaker com mock server.
- `server/go.mod`: Adição de `gobreaker` e `consul/api`.

### 6.2. Change Log
- **Fix (Critical):** Resolvido risco real de panic no startup. Agora o Dispatcher e o Watcher possuem fallbacks seguros caso o Consul esteja offline.
- **Fix (High):** Adicionados testes de unidade e integração para validar a lógica do Circuit Breaker e do Consul KV.
- **Fix (High):** Implementada interface `OutboxRepository` para desacoplar o dispatcher do banco de dados e facilitar testes.
- **Fix (Medium):** Corrigido Circuit Breaker; agora utiliza TODAS as configurações do Consul KV (`webhook_open_duration_seconds`, `webhook_success_threshold`).
- **Fix (Medium):** Removido efeito colateral no `httpClient`; timeouts agora são aplicados via contexto por requisição.
- **Improvement:** Melhorada a responsividade do shutdown do Dispatcher utilizando `select` com `ctx.Done()`.

