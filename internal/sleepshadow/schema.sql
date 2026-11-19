CREATE SCHEMA IF NOT EXISTS sleep_shadow;

CREATE TABLE IF NOT EXISTS sleep_shadow.profile (
    id text PRIMARY KEY
);
ALTER TABLE sleep_shadow.profile ADD COLUMN IF NOT EXISTS source_revision bigint NOT NULL DEFAULT 0;
CREATE TABLE IF NOT EXISTS sleep_shadow.candidate (
    profile text REFERENCES sleep_shadow.profile, id text, revision bigint NOT NULL,
    wake_date date NOT NULL, priority integer NOT NULL, snapshot jsonb NOT NULL,
    PRIMARY KEY (profile, id)
);
CREATE TABLE IF NOT EXISTS sleep_shadow.pending (
    profile text REFERENCES sleep_shadow.profile, candidate text, revision bigint,
    wake_date date NOT NULL, snapshot jsonb NOT NULL,
    PRIMARY KEY (profile, candidate, revision)
);
-- NULL marks a packet queued before the refresh binding was recorded.
ALTER TABLE sleep_shadow.pending ADD COLUMN IF NOT EXISTS refresh_schema text;
CREATE TABLE IF NOT EXISTS sleep_shadow.legacy_alias (
    profile text, candidate text, source_epoch text, source text, generation text,
    PRIMARY KEY (profile, source_epoch, source, generation, candidate),
    FOREIGN KEY (profile, candidate) REFERENCES sleep_shadow.candidate
);
CREATE TABLE IF NOT EXISTS sleep_shadow.legacy_receipt (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    profile text REFERENCES sleep_shadow.profile,
    classification text NOT NULL, payload jsonb NOT NULL
);
CREATE TABLE IF NOT EXISTS sleep_shadow.daily (
    profile text, wake_date date, candidate text NOT NULL, revision bigint NOT NULL,
    sleep_seconds double precision NOT NULL, stages jsonb NOT NULL,
    PRIMARY KEY (profile, wake_date),
    UNIQUE (profile, candidate),
    FOREIGN KEY (profile, candidate) REFERENCES sleep_shadow.candidate
);
ALTER TABLE sleep_shadow.daily
    ADD COLUMN IF NOT EXISTS deep_seconds double precision,
    ADD COLUMN IF NOT EXISTS rem_seconds double precision,
    ADD COLUMN IF NOT EXISTS core_seconds double precision,
    ADD COLUMN IF NOT EXISTS unspecified_seconds double precision,
    ADD COLUMN IF NOT EXISTS awake_seconds double precision,
    ADD COLUMN IF NOT EXISTS stage_conflict boolean;

CREATE TABLE IF NOT EXISTS sleep_shadow.recalculation (
    profile text REFERENCES sleep_shadow.profile, wake_date date, generation bigint NOT NULL,
    PRIMARY KEY (profile, wake_date)
);

CREATE TABLE IF NOT EXISTS sleep_shadow.energy_current (
    profile text NOT NULL REFERENCES sleep_shadow.profile,
    tenant_schema text NOT NULL,
    local_date date NOT NULL,
    state text NOT NULL CHECK (state IN ('fresh', 'estimated', 'stale')),
    bank integer NOT NULL,
    drain_delta integer NOT NULL,
    restore_delta integer NOT NULL,
    formula_version integer NOT NULL,
    flags text[] NOT NULL,
    calculated_at timestamptz NOT NULL,
    PRIMARY KEY (profile, tenant_schema)
);
-- Previously published current rows are reconciled once on the next enable.
ALTER TABLE sleep_shadow.energy_current ADD COLUMN IF NOT EXISTS sleep_generations jsonb;

CREATE TABLE IF NOT EXISTS sleep_shadow.rebuild_checkpoint (
    profile text NOT NULL REFERENCES sleep_shadow.profile,
    tenant_schema text NOT NULL,
    version text NOT NULL,
    source_revision bigint NOT NULL,
    through_date date NOT NULL,
    first_date date,
    next_date date,
    phase text NOT NULL CHECK (phase IN ('sleep', 'derived', 'done')),
    PRIMARY KEY (profile, tenant_schema)
);
ALTER TABLE sleep_shadow.rebuild_checkpoint ADD COLUMN IF NOT EXISTS input_fingerprint text;
ALTER TABLE sleep_shadow.rebuild_checkpoint ADD COLUMN IF NOT EXISTS observation_pending boolean NOT NULL DEFAULT false;

CREATE TABLE IF NOT EXISTS sleep_shadow.rebuilt_readiness (
    profile text NOT NULL REFERENCES sleep_shadow.profile,
    tenant_schema text NOT NULL,
    local_date date NOT NULL,
    rebuild_version text NOT NULL,
    result jsonb NOT NULL,
    PRIMARY KEY (profile, tenant_schema, local_date)
);
