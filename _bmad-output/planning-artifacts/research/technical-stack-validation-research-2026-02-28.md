# Pesquisa Tecnica: Validacao das Stacks do Nexus Event Gateway

**Data:** 2026-02-28
**Projeto:** distribuid-system-simulator (Nexus Event Gateway)
**Objetivo:** Avaliar se as tecnologias definidas no blueprint sao de fato as melhores escolhas para cada camada da arquitetura.

---

## 1. Core Server: Go vs Rust vs Java

### Contexto do Blueprint
O blueprint define **Go (Golang)** como consumer Kafka de alta performance com logica de idempotencia atomica.

### Comparacao

| Criterio | Go | Rust | Java |
|---|---|---|---|
| **Latencia Kafka** | Baixa (I/O async nativo) | Mais baixa (~30% melhor) | Moderada (JVM warmup) |
| **Memoria** | 100-320 MB | 50-80 MB (2-4x menos) | 200-500 MB+ |
| **Tempo de compilacao** | ~2s (imbativel) | 5-15 min | N/A (JIT) |
| **Ecossistema Kafka** | franz-go (puro Go, 4x mais rapido que confluent-kafka-go) | rdkafka bindings | Kafka Streams (mais maduro) |
| **Curva de aprendizado** | Baixa | Alta (borrow checker) | Media |
| **Concorrencia** | Goroutines (excelente) | Tokio (excelente, mais complexo) | Virtual Threads (Java 21+) |

### Bibliotecas Go para Kafka
- **franz-go**: Puro Go, 4x mais rapido que confluent-kafka-go no producing, 10-20x no consuming. Suporte completo a transacoes. **Recomendado.**
- **confluent-kafka-go**: Wrapper do librdkafka (C), dificulta cross-compilation.
- **sarama**: Abandonado, muitas armadilhas.

### Veredicto: GO E A ESCOLHA CORRETA

Go oferece o melhor equilibrio para este projeto:
- Performance excelente para workloads I/O-bound (Kafka consumer)
- Goroutines sao ideais para gerenciar buffers e conexoes concorrentes
- Compilacao rapida acelera ciclos de desenvolvimento
- franz-go e uma biblioteca madura e performatica

**Rust** seria superior em performance bruta e memoria, mas a complexidade de desenvolvimento nao se justifica para um consumer Kafka que e essencialmente I/O-bound. **Java** tem o ecossistema mais maduro (Kafka Streams), mas o overhead de memoria e latencia de startup nao compensam.

**Sugestao de melhoria no tasks.md:** Task 3.1 deveria especificar **franz-go** ao inves de "confluent-kafka-go ou sarama".

---

## 2. Agentes: LangGraph vs CrewAI vs AutoGen

### Contexto do Blueprint
O blueprint define **Python (LangGraph)** para geracao de `plan_id` e injecao de Trace Context.

### Comparacao

| Criterio | LangGraph | CrewAI | AutoGen |
|---|---|---|---|
| **Modelo de execucao** | Grafo de estados (stateful) | Equipes com papeis | Conversacional |
| **Controle de fluxo** | Preciso (branching, loops, recovery) | Delegacao automatica | Debates entre agentes |
| **Persistencia de estado** | Nativa (checkpoints) | Limitada | Limitada |
| **Complexidade** | Media-alta | Baixa | Media |
| **Melhor para** | Workflows complexos e deterministicos | Automacao rapida | Brainstorming, decisoes em grupo |
| **Comunidade/Docs** | Melhor documentacao | Crescendo rapido | Microsoft-backed |

### Veredicto: LANGGRAPH E A ESCOLHA CORRETA

Para o Nexus Event Gateway, LangGraph e a escolha ideal porque:
- O Planner precisa gerar **sequencias deterministicas** de eventos com `plan_id` e `seq_id` — isso e um **grafo de estados**, nao uma conversa
- **Persistencia de estado nativa** permite recovery de planos parcialmente executados
- Controle fino sobre branching (ex: decidir se faz `ABORT_PLAN`) e essencial
- Integra naturalmente com o ecossistema LangChain para futuros LLM calls

**CrewAI** seria overkill (nao precisa de "equipes" para um planner unico). **AutoGen** e conversacional demais para workflows deterministicos.

**Alerta:** A tendencia do mercado em 2026 e convergencia para modelos baseados em grafos — CrewAI e AutoGen v0.4 ja estao adotando esse padrao.

---

## 3. Mensageria: Kafka vs NATS vs Pulsar (+ Redpanda)

### Contexto do Blueprint
O blueprint define **Apache Kafka** para transporte persistente com ordenacao por chave (`order_id`).

### Comparacao

| Criterio | Kafka | Pulsar | NATS | Redpanda |
|---|---|---|---|---|
| **Exactly-Once** | Sim (transacoes + idempotencia) | Sim (per-message ack) | Nao nativo | Sim (compativel Kafka) |
| **Ordenacao por chave** | Sim (partitions) | Sim (partitions) | Limitada | Sim (compativel Kafka) |
| **Throughput** | 10k+ msg/s facilmente | Comparavel | Menor em alto throughput | Ate 10x menor latencia tail |
| **Operacional** | KRaft simplificou muito | Complexo (BookKeeper) | Simples | Mais simples que Kafka |
| **Ecossistema** | Imenso, maduro | Crescendo | Menor para event streaming | Compativel com ecossistema Kafka |
| **Stream Processing** | Kafka Streams, ksqlDB | Pulsar Functions | Nao | Via Kafka Streams |

### Redpanda como alternativa
- Compativel com API Kafka (drop-in replacement)
- Escrito em C++ (Seastar framework), latencias P99.9 ate 10x menores
- **Porem:** benchmarks independentes mostram degradacao com muitos producers e em execucoes longas (>12h)
- Menos maduro em producao que Kafka

### Veredicto: KAFKA E A ESCOLHA CORRETA

Para exactly-once com ordenacao por `order_id`, Kafka e a escolha mais segura:
- **Exactly-once semantics** mais maduras e testadas em producao
- KRaft eliminou a dependencia do ZooKeeper (simplificacao operacional)
- Ecossistema imenso de ferramentas e bibliotecas
- RNF02 do blueprint (10.000 eventos/s) e facilmente atingivel

**Pulsar** seria uma alternativa valida para cloud-native, mas adiciona complexidade operacional (BookKeeper). **NATS** nao tem exactly-once nativo — desqualificado para RF03. **Redpanda** e promissor mas tem caveats de performance em producao prolongada.

**Sugestao:** Considerar Redpanda como alternativa futura para reducao de latencia, mantendo Kafka como escolha atual.

---

## 4. Estado Atomico: Redis + Lua vs Alternativas

### Contexto do Blueprint
O blueprint define **Redis Cluster** com **Scripts Lua** para controle de sequencia atomica e evitar race conditions.

### Analise

| Criterio | Redis + Lua | Redis MULTI/EXEC | Postgres Advisory Locks | DynamoDB Conditional |
|---|---|---|---|---|
| **Atomicidade** | Total (single-thread exec) | Parcial (interleaving possivel) | Sim (mas lento) | Sim |
| **Latencia** | Sub-ms | Sub-ms | 1-5ms | 5-15ms |
| **Throughput** | +30% vs comandos separados | Baseline | Muito menor | Menor |
| **Cluster support** | Sim (com hash tags) | Sim | N/A | Nativo |
| **Complexidade** | Media (Lua scripts) | Baixa | Baixa | Baixa |

### Limitacoes importantes no Redis Cluster

- **Lua scripts no Cluster** exigem que TODAS as chaves estejam no mesmo hash slot
- O blueprint ja endereca isso com **Hash Tags** (`{order_id}:seq`, `{order_id}:buffer`)
- Isso e correto: como o controle e por `order_id`, todas as chaves de um pedido caem no mesmo slot

### Veredicto: REDIS + LUA E A ESCOLHA CORRETA

Nao ha alternativa que combine:
- Atomicidade total (Lua executa como operacao unica)
- Latencia sub-milissegundo
- +30% throughput vs comandos separados
- Logica condicional server-side (check-and-set do `seq_id`)

O blueprint acerta ao usar hash tags para garantir co-localizacao de chaves por `order_id`. A logica `check_and_set_seq.lua` descrita na Secao 3A e o padrao correto.

**Atencao:** Scripts Lua longos bloqueiam o Redis. O script do blueprint e simples (GET + comparacao + SET), entao nao ha risco.

---

## 5. Coordenacao: Etcd vs Consul vs ZooKeeper

### Contexto do Blueprint
O blueprint define **Etcd** para Service Discovery e configuracoes de Circuit Breaker.

### Comparacao

| Criterio | Etcd | Consul | ZooKeeper |
|---|---|---|---|
| **Consenso** | Raft | Raft | ZAB |
| **Consistencia** | Forte (CP) | Forte (CP) | Forte (CP) |
| **Service Discovery** | Basico (watch keys) | Nativo + Health Checks | Via ephemeral nodes |
| **Service Mesh** | Nao | Consul Connect | Nao |
| **Multi-datacenter** | Manual | Nativo | Manual |
| **UI** | Nao nativa | Web UI embutida | Nao nativa |
| **Complexidade** | Baixa | Media | Alta |
| **Kubernetes** | Backend nativo (ja incluso) | Integracao via Helm | Legado |

### Veredicto: ETCD E ADEQUADO, MAS CONSUL SERIA SUPERIOR

**Etcd** funciona para o caso de uso, mas **Consul** ofereceria vantagens claras:

- **Health Checks nativos** — O blueprint menciona Circuit Breaker, e Consul tem health checks embutidos que Etcd nao tem
- **Service Discovery mais rico** — DNS-based discovery vs. watch de chaves
- **Web UI** — facilita operacao e debugging
- **Service Mesh (Connect)** — caso o projeto escale para multiplos servicos

**Etcd** faz sentido se o projeto rodar em **Kubernetes** (ja vem embutido). Se for **Docker Compose puro** (como o tasks.md indica), Consul seria mais pratico.

**ZooKeeper** esta em declinio — Kafka ja eliminou a dependencia dele com KRaft.

**Sugestao de melhoria:** Avaliar se Consul nao seria melhor para o contexto Docker Compose do projeto, ou se Kubernetes esta no roadmap (nesse caso Etcd ja vem "de graca").

---

## 6. Resumo Executivo

| Camada | Escolha do Blueprint | Avaliacao | Alternativa a considerar |
|---|---|---|---|
| **Core Server** | Go | CORRETA | franz-go como client lib |
| **Agentes** | LangGraph | CORRETA | - |
| **Mensageria** | Kafka | CORRETA | Redpanda (futuro) |
| **Estado Atomico** | Redis + Lua | CORRETA | - |
| **Coordenacao** | Etcd | ADEQUADA | Consul (se nao usar K8s) |
| **Source of Truth** | PostgreSQL | CORRETA | - |

### Conclusao

O blueprint acerta em **5 de 6 escolhas**. A unica que merece revisao e **Etcd vs Consul**, dependendo de o projeto usar Kubernetes ou ficar em Docker Compose.

As melhorias recomendadas para o `tasks.md`:
1. Especificar **franz-go** como client Kafka em Go (Task 3.1)
2. Adicionar avaliacao de **Consul vs Etcd** antes de implementar coordenacao
3. Detalhar a Task 3.2 com testes isolados do script Lua

---

## Fontes

- [Go vs Java 2026 Performance](https://backendbytes.com/articles/go-vs-java-2026-performance-showdown/)
- [Go vs Rust Microservices 2026](https://writerdock.in/blog/go-vs-rust-for-microservices-a-2026-performance-benchmark)
- [Rust vs Go vs Java Concurrency 2025](https://medium.com/@Krishnajlathi/rust-vs-go-vs-java-the-2025-concurrency-benchmark-cage-match-943e53d04b8d)
- [franz-go - Pure Go Kafka Library](https://github.com/twmb/franz-go)
- [LangGraph vs CrewAI vs AutoGen 2026](https://o-mega.ai/articles/langgraph-vs-crewai-vs-autogen-top-10-agent-frameworks-2026)
- [AI Agent Frameworks Compared 2026](https://openagents.org/blog/posts/2026-02-23-open-source-ai-agent-frameworks-compared)
- [CrewAI vs LangGraph vs AutoGen - DataCamp](https://www.datacamp.com/tutorial/crewai-vs-langgraph-vs-autogen)
- [Kafka vs Pulsar vs NATS](https://risingwave.com/blog/kafka-pulsar-and-nats-a-comprehensive-comparison-of-messaging-systems/)
- [Kafka vs Pulsar vs NATS 2026](https://aws.plainenglish.io/spring-boot-with-3-event-brokers-kafka-vs-pulsar-vs-nats-which-one-wins-in-2026-7f3dc83d3df8)
- [Redpanda vs Kafka Performance](https://www.redpanda.com/blog/redpanda-vs-kafka-performance-benchmark)
- [Redpanda vs Kafka - Analise Independente](https://jack-vanlightly.com/blog/2023/5/15/kafka-vs-redpanda-performance-do-the-claims-add-up)
- [Redis Lua Scripting - Atomic Operations](https://medium.com/@harishsingh8529/atomic-fast-and-scalable-lua-scripts-are-redis-best-kept-secret-4a1a372a40ec)
- [Redis Cluster Hash Tags](https://redis.io/docs/latest/operate/oss_and_stack/management/scaling/)
- [Etcd vs Consul vs ZooKeeper](https://medium.com/@karim.albakry/in-depth-comparison-of-distributed-coordination-tools-consul-etcd-zookeeper-and-nacos-a6f8e5d612a6)
- [Consul vs ZooKeeper Deep Dive](https://www.wallarm.com/cloud-native-products-101/consul-vs-zookeeper-service-discovery)
- [Etcd vs Other Key-Value Stores](https://etcd.io/docs/v3.3/learning/why/)
- [Message Brokers 2025](https://medium.com/@BuildShift/kafka-is-old-redpanda-is-fast-pulsar-is-weird-nats-is-tiny-which-message-broker-should-you-32ce61d8aa9f)
