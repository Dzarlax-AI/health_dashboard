"""Go/dbt contract fixtures for complete Readiness with frozen reader inputs."""
import json
import math

from readiness_hrv_fixture import ANCHOR, CASES, RHR, SLEEP

HRV = {s["snapshot_id"]: s for s in CASES}
COLUMNS = """
    case_id text, profile text, target_date date, reader text, hrv_snapshot_id text,
    rhr double precision[], sleep double precision[], rhr_present boolean, sleep_present boolean,
    low_sleep_quality boolean, hrv_provisional boolean, hrv_samples integer,
    headline text, illness text
"""


def case(name, hrv_id, **fields):
    hrv = HRV[hrv_id]
    return dict(case_id=name, profile=hrv["profile"], target_date=ANCHOR.isoformat(),
                reader=hrv["reader"], hrv_snapshot_id=hrv_id,
                **({"rhr": RHR, "sleep": SLEEP, "low_sleep_quality": False,
                    "hrv_provisional": False, "hrv_samples": 4, "rhr_present": True,
                    "sleep_present": True, "headline": "", "illness": "",
                    "overnight_present": False} | fields),
                hrv=hrv["values"], hrv_present=hrv["evidence_present"],
                captured=hrv["snapshot_state"] == "captured")


def score_cases():
    morning, history = "morning_known_at_decision", "before_late_correction"
    cases = [
        case("history_all", history), case("morning_all", morning),
        case("history_missing_anchor_hrv", "missing_target_day"),
        case("history_missing_all", "missing_target_day", rhr=[], sleep=[]),
        case("history_corrected", "after_late_correction"),
        case("history_withdrawn", "after_last_point_deletion"),
        case("morning_missing_hrv", "morning_missing_evidence"),
        case("morning_irrecoverable", "morning_unrecoverable"),
        case("morning_invalid", "invalid_snapshot_state"),
        case("missing_rhr", morning, rhr_present=False, overnight_present=True),
        case("missing_sleep", morning, sleep_present=False),
        case("sleep_quality", morning, low_sleep_quality=True),
        case("provisional", morning, hrv_provisional=True),
        case("sparse", morning, hrv_samples=2),
        case("stress", morning, headline="stress"),
        case("sleep_debt", morning, headline="sleep_debt"),
        case("illness_moderate", morning, illness="moderate"),
        case("illness_high", morning, illness="high"),
        case("ordered_caps", morning, illness="high", low_sleep_quality=True,
             hrv_provisional=True, headline="stress"),
        case("quality_after_missing", morning, rhr_present=False, low_sleep_quality=True),
    ]
    for n in (8, 9):
        cases.append(case(f"rhr_{n}", "eight_insufficient", rhr=RHR[:n], sleep=[]))
        cases.append(case(f"sleep_{n}", "eight_insufficient", rhr=[], sleep=SLEEP[:n]))
    for hours in (4.9, 5.0, 5.5, 6.0, 9.49, 9.5, 10.0):
        cases.append(case(f"duration_{hours}", history, rhr=[], sleep=[hours]*7+[7.0,8.0]))
    for spread in (0, 1.0, 1.6, 3.0):
        cases.append(case(f"consistency_{spread}", history,
                          sleep=[7-spread,7+spread]*3+[7,7,8]))
    # Reach both score clamps and every side of the band cutoffs using RHR alone.
    for target in (0, 49, 50, 79, 80, 100):
        recent = 60 - (target - 70) / 15
        cases.append(case(f"band_{target}", "eight_insufficient",
                          rhr=[recent]*7+[59,61], sleep=[]))
    return cases


def prepare(psql, container, cases):
    payload = json.dumps(cases).replace("'", "''")
    psql(container, f"""
        CREATE TABLE source_data.readiness_score_inputs ({COLUMNS}, PRIMARY KEY(profile,case_id));
        INSERT INTO source_data.readiness_score_inputs
        SELECT * FROM jsonb_to_recordset('{payload}'::jsonb) AS s({COLUMNS});
        GRANT SELECT ON source_data.readiness_score_inputs TO pilot_dbt;
    """)


def verify(run, psql, container, cases, lab, env):
    source = lab / "readiness-score-input.json"
    output = lab / "readiness-score-oracle.json"
    source.write_text(json.dumps(cases))
    run(["go","test","./internal/health","-run","^TestReadinessScoreShadowOracle$","-count=1"],
        env=env | {"READINESS_SCORE_ORACLE_INPUT": str(source),"READINESS_SCORE_ORACLE_OUTPUT": str(output)})
    expected = json.loads(output.read_text())
    rows = json.loads(psql(container, "SELECT json_agg(r) FROM dbt_output.semantic_readiness_score_v1 r;", capture=True))
    if len(rows) != len(expected) or {r["case_id"] for r in rows} != set(expected):
        raise AssertionError("score case identities differ")
    for row in rows:
        name = row["case_id"]
        for key, want in expected[name].items():
            got = row[key]
            if key in ("weighted_sum", "available_weight", "hrv_contribution", "rhr_contribution", "sleep_contribution") and want is not None:
                ok = got is not None and math.isclose(got,want,rel_tol=1e-9,abs_tol=1e-9)
            else:
                ok = got == want
            if not ok:
                raise AssertionError(f"{name}.{key}: dbt {got!r}, Go {want!r}")
        if name.startswith("band_") and row["display_score"] != int(name.split("_")[1]):
            raise AssertionError(f"{name}: fixture did not exercise requested band boundary")
    reasons = {r["cap_reason"] for r in rows}
    required = {"missing_same_day_evidence","sleep_quality_low","hrv_provisional","hrv_sparse",
                "stress_headline","sleep_debt_headline","illness_suspicion_high","illness_suspicion_moderate"}
    if not required.issubset(reasons):
        raise AssertionError(f"unexercised readiness caps: {required - reasons}")
    print(f"PASS: {len(rows)} composite Readiness snapshots: unrounded weights, raw and displayed score, band, confidence and cap reason")
