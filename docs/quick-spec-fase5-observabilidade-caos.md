# 🚀 Tech Spec: Fase 5 - Observabilidade e Prova de Conceito (Nexus Event Gateway)

Este documento detalha a implementação da stack de observabilidade distribuída e a criação de uma bateria de testes de caos para validar as garantias de **Exactly-Once** e resiliência do Nexus Event Gateway.

## 1. Objetivos (Scope)
- **OBS-01:** Centralizar a visualização de traces e logs no Grafana.
- **OBS-02:** Garantir a propagação íntegra do W3C TraceContext do Agente (Python) ao Processador (Go).
- **CAOS-01:** Desenvolver um script de Chaos Engineering para simular falhas reais de infraestrutura.
- **CAOS-02:** Validar a recuperação de estado (idempotência e buffers) sob condições extremas.

## 2. Investigação Técnica (Findings)
- **Status Atual:** 
    - O Agente Python e o Server Go já possuem instrumentação OpenTelemetry básica (SDK e OTLP Exporters).
    - O Jaeger All-in-One já está configurado no `docker-compose.yml` ouvindo gRPC (4317).
    - Falta o **Grafana** para unificar a experiência de monitoramento.
    - O script de Chaos Test precisa interagir com a API do Docker para simular quedas de serviço e latência de rede.
    - O fluxo de `ABORT_PLAN` precisa ser validado visualmente nos traces (marcando spans como Error quando um plano é abortado).

## 3. Plano de Implementação

### 3.1. Infraestrutura de Observabilidade
- **Grafana:** Adicionar ao `docker-compose.yml` com provisionamento automático de DataSource (Jaeger).
- **OTel Collector (Opcional):** Avaliar se necessário; por enquanto, Agent e Server enviarão direto para o Jaeger via gRPC para simplificar.
- **Logs:** Integrar logs estruturados (`slog` no Go, `logging` com JSON no Python) para correlação com `TraceID`.

### 3.2. Chaos Engineering (The Gauntlet)
- **Script:** `scripts/chaos_test.py` (Python) utilizando a biblioteca `docker`.
- **Cenários de Teste:**
    1. **Kafka Rebalance:** Derrubar um broker (se houver cluster) ou forçar reconexão do consumer durante o processamento.
    2. **Redis Outage:** Derrubar o Redis enquanto eventos fora de ordem estão no buffer.
    3. **Postgres Latency:** Introduzir latência na transação ACID para testar timeouts e retentativas do Kafka.
    4. **Sequence Gaps:** Produzir eventos 1, 3, 4 (pular 2) e validar o Waiting Room no Redis + envio para DLQ após TTL.
    5. **Zombie Events:** Produzir eventos de um `plan_id` que já recebeu um `ABORT_PLAN`.

## 4. Stories de Implementação (BMAD Format)

### [QS-STORY-01] Unificação com Grafana e Provisionamento
- **Contexto:** Precisamos de um único painel para correlacionar o que o Agente planejou com o que o Gateway executou.
- **Ações:**
    - Atualizar `docker-compose.yml` com a imagem `grafana/grafana-oss`.
    - Criar `monitoring/grafana/datasources.yaml` apontando para o Jaeger.
    - Configurar dashboard básico para visualizar taxa de sucesso de planos vs. abortos.
- **Critérios de Aceite:** Acessar `localhost:3000` e conseguir consultar traces do Jaeger dentro do Grafana.

### [QS-STORY-02] Refinamento da Propagação de Trace Context
- **Contexto:** Alguns spans intermediários (como a execução do Script Lua no Redis) podem estar "orfãos" ou desconectados.
- **Ações:**
    - No Server Go: Garantir que o span do `check_and_set_seq.lua` seja filho do span `process-event`.
    - No Agent Python: Adicionar atributos do LangGraph (node name) aos spans.
    - Adicionar `traceparent` aos logs estruturados (Correlation ID).
- **Critérios de Aceite:** No Jaeger, um trace deve começar em `run-plan` (Agent) e terminar no `ProcessEvent` (Go/DB) de forma contínua.

### [QS-STORY-03] Implementação do Chaos Test Script
- **Contexto:** A prova de conceito final exige evidências de que o sistema não duplica pedidos mesmo sob falhas.
- **Ações:**
    - Criar `scripts/chaos_test.py`.
    - Implementar lógica de "Oráculo": O script envia 100 pedidos, introduz caos, e depois valida se o Postgres tem exatamente 100 registros com os valores corretos.
    - Gerar relatório de falhas/sucessos ao final.
- **Critérios de Aceite:** O script deve rodar de ponta a ponta e reportar "Consistency: OK" após uma rodada de "Sequence Gaps" e "Redis Restart".

## 5. Riscos e Mitigações
- **Falsos Positivos no Caos:** O script de teste pode falhar por problemas no ambiente Docker, não na lógica do sistema. *Mitigação: Implementar retentativas no oráculo e validação de saúde pré-teste.*
- **Performance de Observabilidade:** Instrumentação gRPC pode adicionar latência em picos de carga. *Mitigação: Manter Jaeger e Grafana em rede isolada se necessário, ou usar amostragem.*

---
*Status: Implemented*
---

## 6. Dev Agent Record (AI)
### 6.1. File List
- `docker-compose.yml`: Adição do Grafana e Jaeger gRPC.
- `agent/src/main.py`: Ajuste fino nos endpoints OTel.
- `server/internal/telemetry/telemetry.go`: Melhoria na extração e criação de spans filhos.
- `scripts/chaos_test.py`: Novo script de validação de consistência.
- `monitoring/grafana/`: Configurações de provisionamento.

### 6.2. Plano de Ação Imediato
1.  Atualizar o `docker-compose.yml`.
2.  Validar se o Jaeger recebe traces do Agente Python rodando fora do Docker (ou via profile).
3.  Implementar o script de Chaos Test básico (Sequence Gaps).
