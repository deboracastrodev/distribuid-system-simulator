"""Estado do plano para o grafo LangGraph."""

from __future__ import annotations

from typing import TypedDict


class PlanState(TypedDict):
    order_id: str           # UUID do pedido
    plan_id: str            # "plan_{uuid4_hex[:12]}"
    user_id: str
    items: list[dict]
    total_amount: float
    currency: str
    current_seq: int        # Contador de sequencia
    events: list[dict]      # Eventos gerados (buffer antes de publish)
    status: str             # "planning" | "publishing" | "done" | "aborted"
