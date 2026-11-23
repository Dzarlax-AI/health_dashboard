#!/usr/bin/env python3
"""Capture Go briefing/stress consumers with the existing synthetic oracle fixture."""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile

PILOT = Path(__file__).resolve().parents[1]
ROOT = PILOT.parents[1]
sys.path.insert(0, str(PILOT / 'scripts'))
from run_briefing_components import fixtures


def main():
    out = os.environ.get('HEALTH_METRIC_AUDIT_DIR')
    if not out:
        raise SystemExit('HEALTH_METRIC_AUDIT_DIR is required')
    with tempfile.TemporaryDirectory(prefix='named-briefing-') as tmp:
        source = Path(tmp) / 'inputs.json'
        source.write_text(json.dumps(fixtures()))
        subprocess.run(['go', 'test', './internal/health', '-run', '^TestBriefingComponentsOracle$', '-count=1'],
                       cwd=ROOT, env={**os.environ, 'BRIEFING_COMPONENTS_FIXTURE': str(source)}, check=True)
        observed = json.loads(Path(str(source) + '.expected').read_text())
    mappings = {
        'sustained_hr_load': ('load', 'health.SustainedHRLoad'),
        'acute_stress': ('acute', 'health.AcuteStress'),
        'sustained_load': ('sustained', 'health.SustainedLoadFlag'),
        'illness_signature': ('illness_signature', 'health.IllnessSignature'),
        'recovery_debt': ('recovery_debt', 'health.RecoveryDebt'),
        'parasympathetic_rebound': ('rebound', 'health.ParasympatheticRebound'),
    }
    results = []
    for index, row in enumerate(observed):
        for metric, (field, reader) in mappings.items():
            results.append({'metric_id': metric, 'key': {'capture': index}, 'value': row[field], 'source': reader, 'profile': 'synthetic'})
        for section in ('activity', 'cardio'):
            results.append({'metric_id': section + '_section_status', 'key': {'capture': index},
                            'value': row['sections'].get(section), 'source': 'health.ComputeBriefing', 'profile': 'synthetic'})
    destination = Path(out) / 'briefing.json'
    destination.parent.mkdir(parents=True, exist_ok=True)
    destination.write_text(json.dumps(results, indent=2, allow_nan=False) + '\n')
    print(f'{len(results)} synthetic reader observations: {destination}')


if __name__ == '__main__':
    main()
