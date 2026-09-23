-- Tabela Principal de Pedidos
CREATE TABLE orders (
    id UUID PRIMARY KEY,
    user_id VARCHAR(50) NOT NULL,
    status VARCHAR(20) NOT NULL CHECK (status IN (
        'pending', 'inventory_validated', 'payment_processed',
        'shipped', 'completed', 'cancelled', 'aborted'
    )),
    total_amount DECIMAL(10, 2),
    plan_id VARCHAR(100),
    last_seq_processed INTEGER DEFAULT 0,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

-- Tabela de Outbox (Garantia de Entrega de Mensagens Externas)
CREATE TABLE outbox (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Ordem de inserção. created_at não serve: vários eventos drenados na
    -- mesma transação recebem o mesmo CURRENT_TIMESTAMP.
    position BIGINT GENERATED ALWAYS AS IDENTITY,
    aggregate_id UUID NOT NULL,
    event_type VARCHAR(50) NOT NULL,
    payload JSONB NOT NULL,
    topic VARCHAR(100) NOT NULL,
    processed BOOLEAN DEFAULT FALSE,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

-- Buffer de reordenação: eventos que chegaram antes do antecessor.
-- Fica no mesmo banco que orders para que aplicar um evento e drenar os
-- consecutivos aconteça numa única transação.
CREATE TABLE pending_events (
    order_id UUID NOT NULL,
    plan_id VARCHAR(100) NOT NULL,
    seq_id INTEGER NOT NULL CHECK (seq_id > 0),
    payload JSONB NOT NULL,
    received_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (order_id, plan_id, seq_id)
);

-- Índice para o sweeper encontrar eventos que esperaram demais pelo antecessor
CREATE INDEX idx_pending_events_received_at ON pending_events(received_at);

-- Índice para performance na leitura do buffer de idempotência
CREATE INDEX idx_orders_last_seq ON orders(id, last_seq_processed);

-- Índice parcial para o Outbox Poller buscar eventos não processados
CREATE INDEX idx_outbox_unprocessed ON outbox(position)
    WHERE processed = FALSE;
