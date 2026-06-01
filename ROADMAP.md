# Roadmap

Health Dashboard is a self-hosted, evidence-oriented health analytics system. The roadmap is intentionally conservative: health signals should become more useful only when their inputs, confidence, and limitations are visible.

This document is directional, not a release promise. GitHub Issues are the source of truth for scoped work.

## Now

- Finish EnergyBank v2 operational hardening: deployment verification, stress observability, and post-rollout cleanup.
- Add focused tests around current scoring and import behavior.
- Keep documentation aligned with shipped methodology so contributors can audit what each score means.
- Improve OSS maintainer workflow: issue templates, PR templates, labels, and contributor guidance.

## Next

- Define a readiness freshness and confidence contract that the dashboard can render without guessing.
- Add confidence and uncertainty indicators for user-facing scores.
- Validate readiness, EnergyBank, and stress signals against subjective check-ins once enough answered days exist.
- Expand anonymized fixtures for Apple Health parsing and import safety.
- Review authentication/session hardening for non-local deployments.

## Later

- Revisit parked readiness experiments only after serving/freshness and monitoring gates are operational.
- Explore workout-derived readiness only after structured workout import exists at meaningful scale.
- Decide whether to add slower CI jobs such as the race detector.
- Consider structured logging only when there is a concrete operational need.

## Explicitly Not Planned

- Medical diagnosis or clinical decision support.
- Fabricating wellness scores when required inputs are missing or stale.
- Uploading raw personal health history to a hosted analytics backend by default.
- Shipping scoring changes from private-data experiments without documenting the evidence boundary.
- Treating population benchmarks as a replacement for personal baselines.

## Working Principles

- Raw data stays preserved and derived layers stay rebuildable.
- Missing, imputed, stale, and low-confidence states should be visible.
- Scoring and calibration changes need tests, methodology notes, and a clear rollback path.
- Public issues should use anonymized language such as Profile A / Profile B, not real tenant names or personal health values.
