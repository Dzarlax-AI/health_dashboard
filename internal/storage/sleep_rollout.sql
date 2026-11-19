CREATE TABLE IF NOT EXISTS sleep_rollout_packets (
    sequence bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    source_id text NOT NULL,
    candidate_id text NOT NULL,
    revision bigint NOT NULL CHECK (revision > 0),
    wake_date date NOT NULL,
    raw_payload bytea NOT NULL,
    candidate jsonb NOT NULL,
    legacy_payload jsonb NOT NULL,
    applied_at timestamptz,
    UNIQUE (source_id,candidate_id,revision)
);
CREATE TABLE IF NOT EXISTS sleep_rollout_candidates (
    profile uuid NOT NULL,
    id text PRIMARY KEY,
    revision bigint NOT NULL,
    wake_date date NOT NULL,
    priority integer NOT NULL,
    snapshot jsonb NOT NULL
);
CREATE TABLE IF NOT EXISTS sleep_rollout_daily (
    wake_date date PRIMARY KEY,
    generation bigint NOT NULL,
    candidate text,
    revision bigint,
    sleep_seconds double precision,
    deep_seconds double precision,
    rem_seconds double precision,
    core_seconds double precision,
    unspecified_seconds double precision,
    awake_seconds double precision,
    stage_conflict boolean
);
CREATE TABLE IF NOT EXISTS sleep_rollout_results (
    day date PRIMARY KEY,
    generation bigint NOT NULL,
    readiness jsonb NOT NULL,
    energy jsonb NOT NULL
);
