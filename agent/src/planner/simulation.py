"""Falhas simuladas do plano: estoque indisponível e pagamento recusado.

Cada decisão é um número em [0, 1) derivado de (seed, passo) por hash. Isso
mantém os nodes do grafo puros e sem estado (nada de objeto RNG no state, que
precisa ser serializável para o LangGraph Studio): o mesmo state sempre toma
as mesmas decisões. Numa execução com vários planos, cada plano recebe a seed
derivada da seed da execução e da sua posição (for_plan), então a mesma seed
reproduz a execução inteira, embora order_id e plan_id sejam novos.
"""

from __future__ import annotations

import hashlib
from dataclasses import asdict, dataclass, replace

INVENTORY_STEP = "inventory"
PAYMENT_STEP = "payment"


@dataclass(frozen=True)
class Simulation:
    inventory_failure_rate: float = 0.0
    payment_rejection_rate: float = 0.0
    seed: str = ""

    def __post_init__(self) -> None:
        for name in ("inventory_failure_rate", "payment_rejection_rate"):
            rate = getattr(self, name)
            if not 0.0 <= rate <= 1.0:
                raise ValueError(f"{name} deve estar entre 0 e 1, recebido {rate}")

    def for_plan(self, index: int) -> Simulation:
        """Simulação do plano na posição index de uma execução."""
        return replace(self, seed=f"{self.seed}:{index}")

    def as_state(self) -> dict:
        return asdict(self)


def roll(seed: str, step: str) -> float:
    """Número em [0, 1) determinístico para (seed, step)."""
    digest = hashlib.sha256(f"{seed}:{step}".encode()).digest()
    return int.from_bytes(digest[:8], "big") / 2**64


def fails(simulation: dict | None, step: str, rate_key: str) -> bool:
    """Decide se o passo falha. Sem simulação, nunca falha."""
    sim = simulation or {}
    return roll(sim.get("seed", ""), step) < sim.get(rate_key, 0.0)
