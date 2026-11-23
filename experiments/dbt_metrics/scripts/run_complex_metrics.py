#!/usr/bin/env python3
"""Run the synthetic complex-metric parity suites and report every lane's result."""
from pathlib import Path
import os
import subprocess
import sys

PILOT = Path(__file__).resolve().parents[1]
ROOT = PILOT.parents[1]
SCRIPTS = PILOT / "scripts"


def main():
    checks = {
        "Go decision, illness evidence and report rules": ["go", "test", "./internal/health", "./internal/notify", "-count=1"],
        "HRV correction, withdrawal and recovery": [sys.executable, SCRIPTS / "run_hrv_transition.py"],
        "Sleep, balance and wake": [sys.executable, SCRIPTS / "run_complex_sleep.py"],
        "Main-night reference, Today, dated Sleep and sent report": ["bash", SCRIPTS / "run_sleep_reference.sh", "--docker"],
        "Readiness components, total and limits": [sys.executable, SCRIPTS / "run_readiness_hrv_shadow.py", "--with-score", "--with-chips"],
        "Personal baselines": ["bash", SCRIPTS / "personal_baseline_shadow.sh"],
        "Workout period and types": ["bash", SCRIPTS / "test_workout_shadow.sh"],
        "Energy transitions and snapshots": ["bash", SCRIPTS / "test_energy_bank_shadow.sh"],
        "Briefing sections, trends and signals": [sys.executable, SCRIPTS / "run_briefing_components.py"],
    }
    results = {}
    env = dict(os.environ, PYTHONDONTWRITEBYTECODE="1", DBT_SEND_ANONYMOUS_USAGE_STATS="false")
    for key in tuple(env):
        if key.startswith("PG") or key in {"DATABASE_URL", "READINESS_TEST_DSN", "HEALTH_DB_TESTS"}:
            env.pop(key)
    for name, command in checks.items():
        print(f"\n== {name} ==", flush=True)
        try:
            results[name] = subprocess.run(command, cwd=ROOT, env=env).returncode
        except FileNotFoundError as err:
            print(f"Missing executable: {err.filename}", file=sys.stderr)
            results[name] = 127
    for name, status in results.items():
        print(f"{'PASS' if status == 0 else 'FAIL'}  {name}")
    return int(any(results.values()))


if __name__ == "__main__":
    raise SystemExit(main())
