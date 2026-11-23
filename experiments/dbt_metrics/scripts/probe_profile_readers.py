#!/usr/bin/env python3
"""Exercise product readers against the database just built by run_profile_graph."""
import json
import os
from pathlib import Path
import subprocess


def main():
    required = ("DATABASE_URL", "PROFILE_SOURCE_SCHEMA", "PROFILE_GRAPH_SCHEMA", "PROFILE_PEER_SCHEMA")
    if any(not os.environ.get(key) for key in required):
        raise SystemExit("run through the disposable profile graph harness")
    cases = {
        "internal/storage": ["TestInstalledProfileDailyReader", "TestInstalledProfileSleepReader",
                             "TestInstalledCatalogValuePage", "TestInstalledCatalogMetricCoverage",
                             "TestInstalledLocalEnergyDecision", "TestInstalledLocalReadinessDecision", "TestInstalledDecisionJournal", "TestInstalledTodayBriefingCapture"],
        "internal/ui": ["TestInstalledProfileSleepTransport",
                        "TestInstalledProfileDailyChart", "TestInstalledProfileCatalogStepsChart"],
        "internal/mcpserver": ["TestInstalledProfileSleepTransport"],
    }
    suite = os.environ.get("PROFILE_SCREEN_SUITE")
    if suite:
        tests = {"sections": ["TestInstalledProfileMetrics", "TestInstalledProfileSections",
                              "TestInstalledProfileSleepReports"], "energy": ["TestInstalledProfileEnergy"],
                 "physical_steps": ["TestInstalledProfilePhysicalSteps"]}[suite]
        cases = {"internal/ui": tests}
        print(f"{os.environ['PROFILE_SOURCE_SCHEMA']} {suite} {os.environ['PROFILE_SCREEN_PHASE']}", flush=True)
    root = Path(__file__).resolve().parents[3]
    for package, tests in cases.items():
        result = subprocess.run(
            ["go", "test", "-json", "-count=1", "-run", "^(" + "|".join(tests) + ")$", "./" + package],
            cwd=root, stdout=subprocess.PIPE, text=True,
        )
        passed = set()
        for line in result.stdout.splitlines():
            event = json.loads(line)
            if event.get("Action") == "pass":
                passed.add(event.get("Test"))
            if event.get("Action") == "output":
                print(event["Output"], end="", flush=True)
        result.check_returncode()
        if missing := set(tests) - passed:
            raise SystemExit("profile reader tests did not execute: " + ", ".join(sorted(missing)))


if __name__ == "__main__":
    main()
