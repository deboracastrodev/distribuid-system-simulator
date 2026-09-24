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
    -- Entrega pelo dispatcher: retry agendado no banco, lease para que duas
    -- instâncias não entreguem a mesma entrada ao mesmo tempo, dead-letter.
    attempts INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    lease_until TIMESTAMP WITH TIME ZONE,
    last_error TEXT,
    dead_at TIMESTAMP WITH TIME ZONE,
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

-- Índices parciais para o dispatcher: entradas pendentes em ordem e a entrada
-- mais antiga pendente de cada pedido (só ela pode ser entregue).
CREATE INDEX idx_outbox_unprocessed ON outbox(position)
    WHERE processed = FALSE AND dead_at IS NULL;
CREATE INDEX idx_outbox_pending_by_aggregate ON outbox(aggregate_id, position)
    WHERE processed = FALSE AND dead_at IS NULL;
