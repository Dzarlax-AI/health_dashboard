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

CREATE TABLE IF NOT EXISTS hdbt_i_decisions (
    metric text NOT NULL,
    decision_key text NOT NULL,
    input_hash text NOT NULL,
    PRIMARY KEY (metric, decision_key, input_hash)
);
CREATE TABLE IF NOT EXISTS hdbt_decision_results (
    metric text NOT NULL,
    decision_key text NOT NULL,
    input_hash text NOT NULL,
    formula_version integer NOT NULL,
    result jsonb NOT NULL,
    PRIMARY KEY (metric, decision_key, input_hash)
);

CREATE TABLE IF NOT EXISTS hdbt_decision_queue (
    metric text NOT NULL,
    decision_key text NOT NULL,
    input_hash text NOT NULL,
    blocked_reason text,
    PRIMARY KEY (metric, decision_key, input_hash),
    FOREIGN KEY (metric, decision_key, input_hash)
        REFERENCES hdbt_decision_inputs(metric, decision_key, input_hash) ON DELETE CASCADE
);

ALTER TABLE hdbt_decision_queue ADD COLUMN IF NOT EXISTS blocked_reason text;

CREATE OR REPLACE FUNCTION hdbt_enqueue_decision() RETURNS trigger
LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog AS $$
BEGIN
    EXECUTE format('insert into %I.hdbt_decision_queue (metric,decision_key,input_hash) values ($1,$2,$3) on conflict do nothing',TG_TABLE_SCHEMA)
        USING NEW.metric,NEW.decision_key,NEW.input_hash;
    RETURN NULL;
END
$$;
REVOKE ALL ON FUNCTION hdbt_enqueue_decision() FROM PUBLIC;
CREATE OR REPLACE TRIGGER hdbt_enqueue_decision
AFTER INSERT ON hdbt_decision_inputs FOR EACH ROW
WHEN ((NEW.metric,NEW.formula_version) IN (('energy_bank',3),('readiness',3),('today_decision',1)))
EXECUTE FUNCTION hdbt_enqueue_decision();

-- This migration runs during provisioning or stopped-service upgrades.
INSERT INTO hdbt_decision_queue(metric,decision_key,input_hash)
SELECT j.metric,j.decision_key,j.input_hash FROM hdbt_decision_inputs j
WHERE (j.metric,j.formula_version) IN (('energy_bank',3),('readiness',3),('today_decision',1))
  AND NOT EXISTS (SELECT FROM hdbt_decision_results r
      WHERE (r.metric,r.decision_key,r.input_hash)=(j.metric,j.decision_key,j.input_hash))
ON CONFLICT DO NOTHING;
