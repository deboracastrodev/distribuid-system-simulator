"""Node functions para o grafo LangGraph do Agent Planner.

Cada node gera um evento, incrementa seq_id e append no buffer de eventos.
"""

from __future__ import annotations

import uuid
from datetime import datetime, timezone, timedelta

from src.models.events import (
    AbortPlanData,
    EventEnvelope,
    InventoryValidatedData,
    OrderCompletedData,
    OrderCreatedData,
    OrderShippedData,
    PaymentProcessedData,
)
from src.planner.state import PlanState


def _make_envelope(state: PlanState, event_type: str, data: dict, seq: int) -> dict:
    """Cria um EventEnvelope validado e retorna como dict para Kafka."""
    envelope = EventEnvelope(
        event_type=event_type,
        plan_id=state["plan_id"],
        seq_id=seq,
        order_id=state["order_id"],
        data=data,
    )
    return envelope.to_kafka_value()


def generate_plan(state: PlanState) -> dict:
    """Inicializa o plano — valida inputs e prepara estado."""
    if not state["items"]:
        return {"status": "aborted"}
    if state["total_amount"] <= 0:
        return {"status": "aborted"}
    return {"status": "planning", "current_seq": 0, "events": []}


def create_order_event(state: PlanState) -> dict:
    """Gera evento OrderCreated (seq 1)."""
    seq = state["current_seq"] + 1
    data = OrderCreatedData(
        user_id=state["user_id"],
        items=state["items"],
        total_amount=state["total_amount"],
        currency=state["currency"],
    )
    event = _make_envelope(state, "OrderCreated", data.model_dump(), seq)
    return {
        "current_seq": seq,
        "events": state["events"] + [event],
    }


def create_inventory_event(state: PlanState) -> dict:
    """Gera evento InventoryValidated (seq 2)."""
    seq = state["current_seq"] + 1
    reserved_until = (datetime.now(timezone.utc) + timedelta(hours=1)).isoformat()
    data = InventoryValidatedData(
        warehouse_id=f"wh_{uuid.uuid4().hex[:8]}",
        all_items_available=True,
        reserved_until=reserved_until,
    )
    event = _make_envelope(state, "InventoryValidated", data.model_dump(), seq)
    return {
        "current_seq": seq,
        "events": state["events"] + [event],
    }


def create_payment_event(state: PlanState) -> dict:
    """Gera evento PaymentProcessed (seq 3)."""
    seq = state["current_seq"] + 1
    data = PaymentProcessedData(
        payment_id=f"pay_{uuid.uuid4().hex[:8]}",
        method="credit_card",
        amount=state["total_amount"],
        status="approved",
    )
    event = _make_envelope(state, "PaymentProcessed", data.model_dump(), seq)
    return {
        "current_seq": seq,
        "events": state["events"] + [event],
    }


def create_shipping_event(state: PlanState) -> dict:
    """Gera evento OrderShipped (seq 4)."""
    seq = state["current_seq"] + 1
    estimated = (datetime.now(timezone.utc) + timedelta(days=5)).isoformat()
    data = OrderShippedData(
        tracking_code=f"TRK{uuid.uuid4().hex[:10].upper()}",
        carrier="correios",
        estimated_delivery=estimated,
    )
    event = _make_envelope(state, "OrderShipped", data.model_dump(), seq)
    return {
        "current_seq": seq,
        "events": state["events"] + [event],
    }


def create_completion_event(state: PlanState) -> dict:
    """Gera evento OrderCompleted (seq 5)."""
    seq = state["current_seq"] + 1
    data = OrderCompletedData(
        completed_at=datetime.now(timezone.utc).isoformat(),
        final_status="delivered",
    )
    event = _make_envelope(state, "OrderCompleted", data.model_dump(), seq)
    return {
        "current_seq": seq,
        "events": state["events"] + [event],
        "status": "publishing",
    }


def abort_plan(state: PlanState) -> dict:
    """Gera evento ABORT_PLAN (tombstone, sem seq_id)."""
    data = AbortPlanData(
        reason="Validacao do plano falhou",
        abort_code="manual",
        aborted_at_seq=state["current_seq"],
    )
    envelope = EventEnvelope(
        event_type="ABORT_PLAN",
        plan_id=state["plan_id"],
        seq_id=None,
        order_id=state["order_id"],
        data=data.model_dump(exclude_none=True),
    )
    return {
        "events": state["events"] + [envelope.to_kafka_value()],
        "status": "aborted",
    }
