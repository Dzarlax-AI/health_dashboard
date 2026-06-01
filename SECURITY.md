# Security Policy

Health Dashboard handles sensitive health data. Please report security issues privately and avoid publishing exploit details before a fix is available.

## Reporting a Vulnerability

Use GitHub's private vulnerability reporting feature for this repository when available. If that is not available, open a minimal public issue that says you have a security report and avoid including secrets, health data, payload samples, or exploit steps.

Please include:

- Affected commit, release, or Docker image tag.
- A concise description of the impact.
- Reproduction steps or a proof of concept using dummy data only.
- Whether authentication, multi-user tenant isolation, API keys, MCP access, imports, or admin endpoints are involved.

## Scope

Examples of in-scope issues:

- Authentication or session bypass.
- API key leakage or weak authorization checks.
- Cross-tenant data access in multi-user mode.
- Unsafe file import handling.
- SQL injection or unsafe query construction.
- Exposure of raw health records, metric points, settings, or AI briefing contents.

Examples usually out of scope:

- Reports that require an already-compromised host.
- Denial-of-service issues without a practical exploit path.
- Findings against third-party services not controlled by this project.

## Data Handling

Do not attach real Apple Health exports, production database dumps, screenshots with personal metrics, API keys, Telegram tokens, or session cookies. Use synthetic data or redacted snippets.

## Supported Versions

Security fixes target the current `main` branch and the latest published Docker image. Older commits are not maintained as separate supported release lines.
