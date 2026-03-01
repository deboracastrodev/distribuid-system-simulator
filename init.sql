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
    aggregate_id UUID NOT NULL,
    event_type VARCHAR(50) NOT NULL,
    payload JSONB NOT NULL,
    topic VARCHAR(100) NOT NULL,
    processed BOOLEAN DEFAULT FALSE,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

-- Índice para performance na leitura do buffer de idempotência
CREATE INDEX idx_orders_last_seq ON orders(id, last_seq_processed);

-- Índice parcial para o Outbox Poller buscar eventos não processados
CREATE INDEX idx_outbox_unprocessed ON outbox(processed, created_at)
    WHERE processed = FALSE;
