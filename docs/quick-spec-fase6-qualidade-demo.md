# 🚀 Tech Spec: Fase 6 - Qualidade, Documentação e Demo (Nexus Event Gateway)

Este documento detalha o plano para finalizar a Fase 6 do projeto, garantindo a robustez do sistema através de testes abrangentes, documentação técnica clara e uma demonstração funcional de ponta a ponta.

## 1. Objetivos (Scope)
- **QUAL-01:** Garantir cobertura de testes unitários para a lógica crítica de sequenciamento em Lua.
- **QUAL-02:** Implementar testes de integração no Server Go validando o fluxo Kafka -> Redis -> Postgres.
- **QUAL-03:** Implementar testes unitários no Agent Python para o fluxo de planejamento do LangGraph.
- **DOC-01:** Criar um `README.md` que sirva como guia definitivo para arquitetura e operação.
- **DEMO-01:** Prover um script de demonstração E2E que prove as garantias de **Exactly-Once** sob carga controlada.

## 2. Investigação Técnica (Findings)
- **Lua Tests:** Existe um rascunho em `server/scripts/lua/lua_test.go`, mas depende de um Redis real e está marcado como incompleto nas tasks.
- **Go Integration:** Não há testes de integração automatizados que subam containers efêmeros.
- **Python Agent:** Falta validação automatizada da geração correta de `plan_id` e `seq_id` no LangGraph.
- **Infrastructure:** O `docker-compose.yml` está funcional, facilitando a criação de uma demo E2E.

## 3. Plano de Implementação

### 3.1. Reforço de Qualidade (Server Go)
- **Lua Unit Testing:** Adicionar `github.com/alicebob/miniredis/v2` ao `go.mod` para permitir testes unitários de Lua sem Redis externo.
- **Consumer Integration:** Criar `server/internal/consumer/consumer_test.go`. Utilizar mocks ou containers para validar o processamento de eventos.

### 3.2. Reforço de Qualidade (Agent Python)
- **Planner Validation:** Criar `agent/tests/test_planner.py` usando `pytest`. Validar:
    - Geração de sequências crescentes.
    - Resiliência do estado no LangGraph.
    - Produção correta de eventos `ABORT_PLAN`.

### 3.3. Demonstração e Documentação
- **Script E2E:** `scripts/e2e_demo.py` que envia 100 planos, monitora o Postgres via query e o Redis via scan, reportando latência e integridade.
- **README Refactor:** Consolidar informações de todos os `quick-spec` anteriores em um `README.md` centralizado com diagramas Mermaid.

## 4. Stories de Implementação (BMAD Format)

### [QS-STORY-01] Testes Unitários de Sequenciamento (Lua)
- **Contexto:** A lógica de `check_and_set_seq.lua` é o coração da ordenação atômica.
- **Ações:**
    - Adicionar `miniredis` ao projeto Go.
    - Refatorar `lua_test.go` para usar `miniredis`.
    - Garantir que `go test ./scripts/lua` passe localmente.
- **Critérios de Aceite:** 100% de sucesso nos casos: OK, Duplicate, Out of Order, Aborted.

### [QS-STORY-02] Testes de Integração Server Go
- **Contexto:** Precisamos garantir que o handler `process-event` integra corretamente Kafka, Redis e Postgres.
- **Ações:**
    - Criar suite de testes de integração em `server/internal/consumer`.
    - Mockar o producer de feedback se necessário.
- **Critérios de Aceite:** Teste que simula a chegada de um evento no consumidor e valida se o registro no Postgres foi criado e o Redis atualizado.

### [QS-STORY-03] Testes de Unidade Agent Python
- **Contexto:** O Agente é responsável pela integridade inicial do plano.
- **Ações:**
    - Instalar `pytest` no `venv`.
    - Criar testes para os nós do LangGraph.
- **Critérios de Aceite:** Validar que `generate_plan` produz o número correto de eventos com IDs únicos.

### [QS-STORY-04] Demo E2E e Documentação Final
- **Contexto:** O projeto precisa ser fácil de rodar e entender.
- **Ações:**
    - Criar `scripts/e2e_demo.py`.
    - Escrever `README.md` completo.
- **Critérios de Aceite:** Script reporta 100% de consistência após o fluxo completo.

## 5. Riscos e Mitigações
- **Timeouts em CI:** Testes de integração podem ser lentos. *Mitigação: Usar tags de build para isolar testes pesados.*
- **Dependência de Docker:** A demo E2E requer todos os serviços de pé. *Mitigação: Adicionar script de health check antes de iniciar a demo.*

---
*Status: Draft*
---

## 6. Dev Agent Record (AI)

### 6.1. File List
- `server/scripts/lua/lua_test.go` (Update)
- `server/internal/consumer/consumer_test.go` (New)
- `agent/tests/test_planner.py` (New)
- `scripts/e2e_demo.py` (New)
- `README.md` (Update/New)

### 6.2. Plano de Ação Imediato
1.  Ajustar `go.mod` para incluir dependências de teste.
2.  Implementar `lua_test.go` with `miniredis`.
3.  Implementar testes básicos do Agente Python.
