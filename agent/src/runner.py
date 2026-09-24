"""Executa planos: roda o grafo e publica cada evento assim que ele é gerado.

O grafo só decide (nodes puros); publicar é trabalho do runner. Com
graph.stream, cada evento sai logo depois do node que o gerou, com um atraso
opcional entre eventos, em vez de o plano inteiro ser publicado de uma vez
no fim.
"""

from __future__ import annotations

import time
import uuid
from dataclasses import asdict, dataclass
from typing import Callable

from opentelemetry import trace

from src.models.events import generate_plan_id
from src.planner.simulation import Simulation

tracer = trace.get_tracer(__name__)


@dataclass
class PlanOutcome:
    order_id: str
    plan_id: str
    seed: str  # seed da simulação deste plano: reproduz as decisões dele
    outcome: str  # "completed" | "aborted"
    abort_reason: str
    events: list[str]  # tipos publicados, em ordem
    last_seq: int  # maior seq_id publicado (0 se nenhum)

    def as_report(self) -> dict:
        return asdict(self)


def build_initial_state(order: dict, simulation: Simulation) -> dict:
    """Estado inicial do plano a partir de um pedido."""
    return {
        "order_id": order.get("order_id", str(uuid.uuid4())),
        "plan_id": generate_plan_id(),
        "user_id": order["user_id"],
        "items": order["items"],
        "total_amount": order["total_amount"],
        "currency": order.get("currency", "BRL"),
        "current_seq": 0,
        "events": [],
        "status": "planning",
        "abort_reason": "",
        "simulation": simulation.as_state(),
    }


def run_plan(
    graph,
    order: dict,
    simulation: Simulation,
    publish: Callable[[dict], None],
    step_delay: float = 0.0,
    sleep: Callable[[float], None] = time.sleep,
) -> PlanOutcome:
    """Executa um plano, publicando cada evento logo que o grafo o gera."""
    state = build_initial_state(order, simulation)
    published: list[dict] = []
    final = state

    with tracer.start_as_current_span("run-plan") as span:
        span.set_attribute("plan.id", state["plan_id"])
        span.set_attribute("order.id", state["order_id"])

        for values in graph.stream(state, stream_mode="values"):
            for event in values["events"][len(published):]:
                if published and step_delay > 0:
                    sleep(step_delay)
                publish(event)
                published.append(event)
            final = values

        outcome = "aborted" if final["status"] == "aborted" else "completed"
        span.set_attribute("plan.outcome", outcome)

    return PlanOutcome(
        order_id=state["order_id"],
        plan_id=state["plan_id"],
        seed=simulation.seed,
        outcome=outcome,
        abort_reason=final.get("abort_reason", "") if outcome == "aborted" else "",
        events=[e["event_type"] for e in published],
        last_seq=max((e.get("seq_id") or 0 for e in published), default=0),
    )
