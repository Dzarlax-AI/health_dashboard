"""Execute the local shadow storage oracle, returning aggregate-only JSON."""
import json
import os
from pathlib import Path
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[3]
ACTUAL = ROOT / 'experiments/dbt_metrics/semantic/hrv_oracle_test.go.txt'
VIRTUAL = ROOT / 'internal/storage/semantic_shadow_hrv_oracle_test.go'

if __name__ == '__main__':
    with tempfile.TemporaryDirectory() as directory:
        overlay = Path(directory) / 'overlay.json'
        overlay.write_text(json.dumps({'Replace': {str(VIRTUAL): str(ACTUAL)}}))
        env = {k: v for k, v in os.environ.items() if not k.startswith('PG') and k != 'DATABASE_URL'}
        env['GOPROXY'] = 'off'
        command = ['go', 'test', '-overlay', str(overlay), './internal/storage', '-run',
                   '^TestSemanticShadowHRVOracle$', '-count=1', '-timeout=5m', '-v']
        result = subprocess.run(command, cwd=ROOT, env=env, capture_output=True, text=True)
        output = [line.removeprefix('SHADOW_HRV_ORACLE ') for line in result.stdout.splitlines()
                  if line.startswith('SHADOW_HRV_ORACLE ')]
        if len(output) == 1:
            print(json.dumps(json.loads(output[0]), indent=2))
        if result.returncode or not output:
            print(result.stderr[-1000:], result.stdout[-1500:], file=__import__('sys').stderr)
            raise SystemExit(result.returncode or 1)
