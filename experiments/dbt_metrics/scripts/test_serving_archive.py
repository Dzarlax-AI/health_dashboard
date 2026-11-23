"""Synthetic checks for the private archive verifier. No Docker daemon or dump is required."""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

from archive_step_oracle import archive_steps
from run_serving_archive import image_once

SCRIPTS = Path(__file__).resolve().parent


class OracleTests(unittest.TestCase):
    def test_missing_and_suppressed_daily_values(self):
        days = [f"2025-01-{n:02}" for n in range(1, 8)]
        points = [
            ["2025-01-01 22:30:00 +0000", "Watch", 42, "ok", "step_count", "hour"],
            ["2025-01-02 00:30:00 +0200", "Watch", 42, "ok", "step_count", "hour"],
            ["2025-01-06 08:00:00 +0000", "Watch", 0, "ok", "step_count", "hour"],
        ]
        prior = dict(zip(days, (900, 600, 19, 35, 40, 10, 0)))

        def query(stmt):
            if "from health.metric_points" in stmt:
                return json.dumps(points)
            if "from health.hourly_metrics" in stmt:
                return "[]"
            self.assertIn("from health.hdbt_step_repairs", stmt)
            self.assertIn("from health.hdbt_step_state", stmt)
            return "\n".join([days[1], days[3], days[4]])

        totals, retained = archive_steps(query, days, prior)
        self.assertEqual({days[1]: 42, days[2]: 19, days[5]: 0, days[6]: 0}, totals)
        self.assertEqual({days[2], days[6]}, retained)


class LauncherTests(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        self.root = Path(temp.name)
        self.bin = self.root / "bin"
        self.bin.mkdir()
        self.scratch = self.root / "scratch"
        self.scratch.mkdir()
        self.dump = self.root / "archive"
        self.dump.write_bytes(b"synthetic")
        self.calls = self.root / "commands"
        self.env = dict(os.environ, TMPDIR=str(self.scratch), CALLS=str(self.calls),
                        PATH=str(self.bin) + os.pathsep + os.environ["PATH"], DOCKER_HOST="unix:///fixture")
        self.stub("docker", """
echo "$*" >> "$CALLS"
case "$1" in
run) echo fixture-id ;;
port) echo 127.0.0.1:40000 ;;
exec) echo 'fs blocks used available'; echo 'fs 0 0 999999999' ;;
stop) exit 1 ;;
rm) exit "${RM_STATUS:-0}" ;;
ps) echo fixture-id ;;
esac
""")

    def stub(self, name, body):
        path = self.bin / name
        path.write_text("#!/bin/sh\n" + body)
        path.chmod(0o700)

    def launch(self):
        return subprocess.run(["bash", str(SCRIPTS / "with_serving_archive.sh"), str(self.dump)],
                              env=self.env, capture_output=True, text=True)

    def test_missing_baseline_fails_before_database_or_scratch(self):
        self.env["GIT_DIR"] = str(self.root / "missing-git")
        result = self.launch()
        self.assertNotEqual(0, result.returncode)
        self.assertIn("Baseline commit", result.stderr)
        self.assertFalse(self.calls.exists())
        self.assertFalse(any(p.is_dir() for p in self.scratch.iterdir()))

    def test_sidecar_cleanup_failure_still_unlinks_dsn(self):
        calls = []

        def docker(args, env, **opts):
            calls.append(args[:3])
            if args[1:3] == ["network", "disconnect"]:
                raise RuntimeError("fixture disconnect failed")
            return '{"status":"published"}'

        with patch("run_serving_archive.invoke", side_effect=docker):
            with self.assertRaisesRegex(RuntimeError, "fixture disconnect"):
                image_once({"SERVING_ARCHIVE_CONTAINER": "a" * 64},
                           {"HEALTH_DBT_TENANT_ID": "fixture"},
                           {"PGUSER": "fixture", "PGPASSWORD": "fixture-only"},
                           self.scratch, "sha256:" + "b" * 64)
        self.assertIn(["docker", "network", "rm"], calls)
        self.assertEqual([], list(self.scratch.iterdir()))

    def test_stop_error_always_removes_local_secrets(self):
        for tool in ("go", "pg_restore", "pg_isready"):
            self.stub(tool, "exit 0")
        self.stub("psql", "cat >/dev/null")
        self.stub("df", "echo 'Filesystem blocks used available'; echo 'fs 0 0 999999999'")
        self.stub("python3", f"""
case "$*" in
*--preflight-baseline) exit 0 ;;
*run_serving_archive.py*) exit 47 ;;
*) exec "{sys.executable}" "$@" ;;
esac
""")
        for rm_status in (0, 1):
            with self.subTest(forced_removal_status=rm_status):
                self.env["RM_STATUS"] = str(rm_status)
                result = self.launch()
                self.assertEqual(47 if rm_status == 0 else 1, result.returncode, result.stderr)
                self.assertIn("rm -fv fixture-id", self.calls.read_text())
                self.assertEqual([], list(self.scratch.iterdir()))


if __name__ == "__main__":
    unittest.main()
