import pytest

from src.planner.simulation import INVENTORY_STEP, PAYMENT_STEP, Simulation, fails, roll


def test_roll_is_deterministic_and_in_unit_interval():
    first = roll("seed", INVENTORY_STEP)
    assert first == roll("seed", INVENTORY_STEP)
    assert 0.0 <= first < 1.0


def test_roll_depends_on_seed_and_step():
    base = roll("seed", INVENTORY_STEP)
    assert base != roll("other", INVENTORY_STEP)
    assert base != roll("seed", PAYMENT_STEP)


def test_failure_rate_is_respected_across_plans_of_a_run():
    run = Simulation(inventory_failure_rate=0.3, seed="dist")
    n = 2000
    failures = sum(
        fails(run.for_plan(i).as_state(), INVENTORY_STEP, "inventory_failure_rate") for i in range(n)
    )
    # Binomial(2000, 0.3): desvio padrão ~20; 5 desvios de tolerância.
    assert abs(failures - 0.3 * n) < 100


def test_for_plan_derives_a_distinct_seed_per_position():
    run = Simulation(inventory_failure_rate=0.5, payment_rejection_rate=0.2, seed="ci")
    plan = run.for_plan(3)
    assert plan.seed == "ci:3"
    assert (plan.inventory_failure_rate, plan.payment_rejection_rate) == (0.5, 0.2)
    assert len({run.for_plan(i).seed for i in range(10)}) == 10


@pytest.mark.parametrize("rate, expected", [(0.0, False), (1.0, True)])
def test_extreme_rates(rate, expected):
    run = Simulation(payment_rejection_rate=rate, seed="x")
    assert all(
        fails(run.for_plan(i).as_state(), PAYMENT_STEP, "payment_rejection_rate") is expected
        for i in range(200)
    )


def test_without_simulation_nothing_fails():
    assert fails(None, INVENTORY_STEP, "inventory_failure_rate") is False
    assert fails({}, PAYMENT_STEP, "payment_rejection_rate") is False


def test_each_rate_only_affects_its_own_step():
    sim = Simulation(inventory_failure_rate=1.0, seed="x").as_state()
    assert fails(sim, INVENTORY_STEP, "inventory_failure_rate")
    assert not fails(sim, PAYMENT_STEP, "payment_rejection_rate")


@pytest.mark.parametrize("field", ["inventory_failure_rate", "payment_rejection_rate"])
@pytest.mark.parametrize("rate", [-0.1, 1.5])
def test_invalid_rates_are_rejected(field, rate):
    with pytest.raises(ValueError, match=field):
        Simulation(**{field: rate})
