# Quick Tech Spec — Melhorias de Robustez na Fase 2 (Python)

## Resumo
Este documento especifica as correções e melhorias de robustez para o Agent Planner (Python) após a revisão adversarial da Fase 2. O objetivo é aumentar a confiabilidade, observabilidade e performance do componente antes de prosseguir para a Fase 3 (Go).

---

## Melhorias e Correções

### 1. Configuração e Ambiente (Ref: Finding 1, 4)
**O que fazer:**
- Mover todas as configurações "hardcoded" para variáveis de ambiente obrigatórias ou com defaults claros em `config.py`.
- Adicionar suporte a TLS/SSL para o OTLP Exporter, configurável via env `OTEL_EXPORTER_OTLP_INSECURE` (default `True` para dev).
- Validar a presença de variáveis críticas no startup do `main.py`.

**Critérios de Aceite:**
- [ ] O agente falha ao iniciar se variáveis críticas (`KAFKA_BOOTSTRAP_SERVERS`) não estiverem definidas ou forem inválidas.
- [ ] Conexão com Jaeger suporta modo seguro (insecure=False) via configuração.

### 2. Performance e Confiabilidade do Producer (Ref: Finding 2, 7)
**O que fazer:**
- Alterar `publish_event` para não bloquear. Remover `producer.poll(0)` de dentro do loop de eventos.
- Chamar `producer.poll(0)` em um loop separado ou após cada `produce()` para processar callbacks sem bloquear o envio do próximo evento.
- No `main.py`, preparar a estrutura para processamento concorrente (ex: `ThreadPoolExecutor` para múltiplos pedidos simultâneos), mesmo que o modo CLI atual seja sequencial.

**Critérios de Aceite:**
- [ ] O throughput de envio de eventos aumenta (validado via logs de timestamp).
- [ ] Callbacks de entrega são processados corretamente sem travar o loop principal.

### 3. Inteligência e Resiliência do Planner (Ref: Finding 3, 10)
**O que fazer:**
- Adicionar validações de negócio nos nodes do LangGraph (ex: verificar estoque fictício ou limites de valor).
- Melhorar o modo `--dry-run`: além de imprimir os eventos, ele deve validar a conectividade básica com as dependências (Kafka/Jaeger) sem enviar dados reais.
- Implementar retry automático no `publish_plan` em caso de falhas transientes de rede antes de desistir do plano.

**Critérios de Aceite:**
- [ ] `--dry-run` reporta erro se o Kafka estiver offline.
- [ ] O grafo aborta corretamente com `ABORT_PLAN` se uma regra de negócio for violada.

### 4. Identificação e Versionamento (Ref: Finding 5, 6)
**O que fazer:**
- Alterar `plan_id` para usar UUID4 completo ou prefixo seguro de 16+ caracteres para evitar colisões.
- Adicionar o campo `schema_version` (default: `"1.0"`) no `EventEnvelope` e em todos os modelos Pydantic.
- Garantir que o `schema_version` seja propagado para o JSON final enviado ao Kafka.

**Critérios de Aceite:**
- [ ] Todos os eventos no Kafka possuem campo `schema_version`.
- [ ] `plan_id` segue o novo padrão de comprimento.

### 5. Observabilidade e Tracing (Ref: Finding 9)
**O que fazer:**
- Refatorar `_build_traceparent`: em vez de criar spans efêmeros, garantir que o contexto de trace do OpenTelemetry seja propagado corretamente do início do processo (`run_plan`) até o producer.
- Garantir que o `traceparent` injetado no Kafka corresponda ao trace ativo no Jaeger para aquele pedido específico.

**Critérios de Aceite:**
- [ ] Traces no Jaeger mostram a árvore completa: `run_plan -> build_graph -> publish_plan`.
- [ ] Não existem mais spans "órfãos" gerados apenas para obter IDs.

---

## Plano de Implementação

| Task | Arquivos Afetados | Prioridade |
|---|---|---|
| Refactor Configs | `config.py`, `main.py` | Alta |
| Async Producer | `kafka_producer.py` | Alta |
| Versioning & IDs | `models/events.py`, `nodes.py` | Média |
| Tracing Cleanup | `kafka_producer.py`, `main.py` | Média |
| Enhanced Dry-run | `main.py` | Baixa |

---

## Validação de Sucesso
- [ ] Execução de um plano completo com sucesso e visualização do trace contínuo no Jaeger.
- [ ] Verificação de eventos no Kafka contendo `schema_version` e `plan_id` longo.
- [ ] Teste de falha de conexão com Kafka resultando em erro claro no `--dry-run`.
