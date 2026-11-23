"""Deterministic, synthetic Readiness HRV snapshots for the shadow pilot."""

from datetime import date, timedelta


ANCHOR = date(2026, 11, 22)


def dated(values: list[float], *, gaps: set[int] | None = None, start: int = 0):
    gaps = gaps or set()
    offsets = (n for n in range(start, 30) if n not in gaps)
    pairs = [(ANCHOR - timedelta(days=next(offsets)), value) for value in values]
    return [day.isoformat() for day, _ in pairs], [value for _, value in pairs]


def case(name: str, values: list[float], *, reader: str = "history",
         gaps: set[int] | None = None, start: int = 0, state: str = "captured",
         evidence: bool = True, rhr: list[float] | None = None,
         sleep: list[float] | None = None, path: str | None = None,
         missing_anchor: bool = False, retained_candidates: list[float] | None = None,
         raw_today: bool = True, repair_today: bool = False):
    dates, values = dated(values, gaps=gaps, start=start)
    candidate_dates, candidate_values = dates, values
    if retained_candidates is not None:
        candidate_dates, candidate_values = dated(retained_candidates)
    if missing_anchor:
        dates, values = [], []
    return dict(snapshot_id=name, profile="synthetic", target_date=ANCHOR.isoformat(),
                reader=reader, reader_path=path or ("history" if reader == "history" else "morning_warm"),
                snapshot_state=state, evidence_present=evidence,
                dates=dates, values=values, candidate_dates=candidate_dates,
                candidate_values=candidate_values, raw_today=raw_today,
                repair_today=repair_today, rhr=rhr or [], sleep=sleep or [])


BASE = [55, 53, 51, 49, 47, 45, 43, 42, 48, 54, 50, 46, 52]
RHR = [59, 60, 61, 62, 61, 60, 59, 63, 65, 62, 64, 61, 66]
SLEEP = [7.4, 7.3, 7.6, 7.2, 7.1, 7.5, 7.4, 6.8, 7.0, 7.2, 6.9, 7.1, 6.7]


CASES = [
    case("eight_insufficient", BASE[:8]),
    case("nine_available", BASE[:9]),
    case("constant_baseline", [50] * 9),
    case("missing_target_day", BASE[:9], start=1, missing_anchor=True),
    case("interior_gap", BASE[:12], gaps={5}),
    case("before_late_correction", BASE),
    case("after_late_correction", [34, *BASE[1:]]),
    case("before_last_point_deletion", BASE[:9]),
    case("after_last_point_deletion", BASE[:8]),
    case("high_direction", [66, *BASE[1:]]),
    case("low_direction", [34, *BASE[1:]]),
    case("multi_component", BASE, rhr=RHR, sleep=SLEEP),
    case("morning_known_at_decision", BASE, reader="morning"),
    case("morning_cached_anchor", BASE, reader="morning", raw_today=False),
    case("morning_cold_known", BASE, reader="morning", path="morning_cold"),
    case("morning_missing_evidence", BASE, reader="morning", evidence=False),
    case("morning_unrecoverable", [], reader="morning", state="unrecoverable",
         retained_candidates=BASE),
    case("invalid_snapshot_state", [], reader="history", state="invalid",
         retained_candidates=BASE),
]

# The visible cached anchor is under repair. Its value must be suppressed;
# older cache rows stay available, while same-day evidence is absent.
repair = case("morning_repair_pending", BASE[1:], reader="morning", start=1,
              evidence=False, raw_today=False, repair_today=True)
repair["candidate_dates"], repair["candidate_values"] = dated(BASE)
CASES.append(repair)
