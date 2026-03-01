"""Pydantic models para eventos do Nexus Event Gateway.

Baseados nos JSON Schemas de schemas/events/.
"""

from __future__ import annotations

import uuid
from datetime import datetime, timezone
from typing import Optional

from pydantic import BaseModel, Field


# --- Data payloads (campo "data" do envelope) ---


class OrderItem(BaseModel):
    product_id: str
    quantity: int = Field(ge=1)
    unit_price: float = Field(ge=0)


class OrderCreatedData(BaseModel):
    user_id: str
    items: list[OrderItem] = Field(min_length=1)
    total_amount: float = Field(ge=0)
    currency: str = "BRL"


class InventoryValidatedData(BaseModel):
    warehouse_id: str
    all_items_available: bool
    reserved_until: str  # ISO 8601


class PaymentProcessedData(BaseModel):
    payment_id: str
    method: str
    amount: float = Field(ge=0)
    status: str  # "approved" | "rejected"


class OrderShippedData(BaseModel):
    tracking_code: str
    carrier: str
    estimated_delivery: str  # ISO 8601


class OrderCompletedData(BaseModel):
    completed_at: str  # ISO 8601
    final_status: str = "delivered"


class AbortPlanData(BaseModel):
    reason: str
    abort_code: str  # "inventory_failed" | "payment_rejected" | "timeout" | "manual"
    aborted_at_seq: Optional[int] = Field(default=None, ge=0)


# --- Envelope ---


class EventEnvelope(BaseModel):
    event_id: str = Field(default_factory=lambda: str(uuid.uuid4()))
    event_type: str
    plan_id: str
    seq_id: Optional[int] = Field(default=None, ge=1)
    order_id: str
    timestamp: str = Field(
        default_factory=lambda: datetime.now(timezone.utc).isoformat()
    )
    data: dict

    def to_kafka_value(self) -> dict:
        """Serializa para envio ao Kafka, omitindo seq_id se None (ABORT_PLAN)."""
        d = self.model_dump()
        if d["seq_id"] is None:
            del d["seq_id"]
        return d


# Mapeamento tipo -> classe de data para validacao
EVENT_DATA_MODELS: dict[str, type[BaseModel]] = {
    "OrderCreated": OrderCreatedData,
    "InventoryValidated": InventoryValidatedData,
    "PaymentProcessed": PaymentProcessedData,
    "OrderShipped": OrderShippedData,
    "OrderCompleted": OrderCompletedData,
    "ABORT_PLAN": AbortPlanData,
}
