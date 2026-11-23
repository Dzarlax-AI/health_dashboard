CREATE TABLE IF NOT EXISTS hdbt_decision_inputs (
    metric text NOT NULL,
    decision_key text NOT NULL,
    input_hash text NOT NULL,
    captured_at timestamptz NOT NULL,
    formula_version integer NOT NULL,
    inputs jsonb NOT NULL,
    result jsonb NOT NULL,
    PRIMARY KEY (metric, decision_key, input_hash)
);
