# Admin User Tabs Design

Date: 2026-05-23

## Goal

The current admin area mixes global settings, user-specific settings, diagnostics, and operational actions. This makes it hard to know whether a control affects the whole installation or one tenant.

Restructure `/admin` around explicit scope:

- `General settings` for installation-wide state.
- One tab per registered user/tenant for settings and operations that affect that user's health data.

Keep the change focused on navigation and scope clarity. Do not redesign the entire dashboard or create a full operator console in this pass.

## Chosen Approach

Use a single `/admin` page with top-level tabs:

```text
General settings | Alexey | Mariia | + Add user
```

`General settings` contains global/system controls:

- Gemini API settings.
- Installation-wide EnergyBank/stress-drain defaults.
- Registered users list.
- Add-user form.
- General system status that is not tied to one tenant.

Each user tab contains tenant-scoped controls:

- Overview/status for that user's schema.
- Telegram token, chat ID, report language, timezone, and report schedule.
- Apple Health import.
- Historical EnergyBank backfill.
- Cache backfill and force backfill.
- Data gaps, data quality audit/fix/digest.
- Subjective check-in coverage.
- Stress validation.
- Readiness redesign operational contract, config, chip calibration, and onboarding runbook.

Long-running or destructive actions live inside the relevant user tab so the operator always sees whose data will be affected before starting the action.

## Non-Admin Settings

Keep `/settings` for non-admin users for now. It remains the simple "my settings" page.

For admin users, `/settings` may stay as-is during the first implementation step or become a shortcut to the current user's tab in `/admin`. Do not remove `/settings` until the tabbed admin page is validated in the browser.

## UI Structure

The page uses two navigation levels:

- Top tabs choose scope: general vs one tenant.
- Inside each user tab, compact section navigation or grouped sections separate `Settings`, `Import`, `Diagnostics`, and `Operations`.

The first implementation can use existing visual primitives from `internal/ui/style.go`:

- existing `admin-section`
- existing `admin-group`
- existing buttons, tables, alerts, and form controls

Avoid a decorative redesign. The main UX improvement is scope visibility and predictable grouping.

## Backend Scope Model

Existing request context already carries the current user's DB/schema. Admin endpoints also have patterns for explicit `schema=` overrides. The tabbed UI should make this explicit:

- General controls call global endpoints or admin endpoints that are intentionally installation-wide.
- User tabs call tenant-scoped endpoints with a schema identifier.
- Endpoints that already accept `schema=` should be reused where possible.
- Endpoints that currently operate only on the request user's context need an admin-only `schema=` path before they can be safely used from another user's tab.

All explicit schema values must be validated against `health_registry.users`. Never trust a raw schema from the browser.

## Endpoint Impact

Likely global endpoints:

- `GET/POST /api/admin/settings` for Gemini config.
- `GET/POST /api/admin/energy-settings` for installation-wide EnergyBank stress-drain defaults.
- `GET/POST /api/admin/users` for user listing and creation.

Likely tenant-scoped endpoints that may need schema support or UI wiring:

- `/api/settings`
- `/api/settings/test-notify`
- `/api/webhook-status`
- `/api/webhook-status/retry`
- `/api/import/upload`
- `/api/import/status`
- `/api/settings/energy-backfill/*`
- `/api/admin/backfill`
- `/api/admin/gaps`
- `/api/admin/quality-audit`
- `/api/admin/quality-fix`
- `/api/admin/quality-digest`
- `/api/admin/checkin-coverage`
- `/api/admin/stress-validation`
- `/api/admin/readiness-redesign/*`
- `/fragments/admin-status`
- `/fragments/admin-readiness-contract`
- `/fragments/admin-readiness-onboarding/*`

If an endpoint performs writes, the implementation must show the selected user/schema in the UI near the action button.

## Migration Plan

Phase 1: Reorganize `/admin` without deleting `/settings`.

- Add top-level tabs.
- Move global admin controls into `General settings`.
- Render user tabs from `/api/admin/users`.
- Reuse current markup where possible.

Phase 2: Add tenant scope to missing endpoints.

- Audit every action that appears inside user tabs.
- Add validated `schema=` support where missing.
- Keep old current-user behavior when `schema=` is absent.

Phase 3: Decide what to do with `/settings`.

- Leave it for non-admin users.
- Optionally redirect admin users to their own tab in `/admin`.
- Only remove duplicate UI after browser validation.

## Testing

Add focused tests for backend scope behavior:

- Admin can target a known tenant schema.
- Unknown schema is rejected.
- Non-admin cannot target another tenant via `schema=`.
- Existing current-user behavior still works when `schema=` is absent.

Add template or handler tests where practical:

- `/admin` renders `General settings` and one tab per registered user.
- Tenant action URLs include the selected schema where required.
- Existing htmx fragments still initialize after injection; current memory notes that fetch plus `innerHTML` does not automatically process htmx attributes.

Run at minimum:

```bash
go test ./internal/ui ./internal/tenants ./internal/registry
go test ./...
```

Before completion, open the local UI in a browser and verify:

- tabs fit on desktop and mobile widths;
- destructive actions visibly show the target user/schema;
- `/settings` still works for current-user settings;
- no mixed global/user controls remain in the same visual group.

## Out Of Scope

- Job history.
- A full multi-tenant operator dashboard.
- Rewriting visual design tokens.
- Removing `/settings`.
- Changing health scoring, EnergyBank formula behavior, or notification semantics.
