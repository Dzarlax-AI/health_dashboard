CREATE TABLE IF NOT EXISTS sleep_shadow.rebuild_input_revision (
    tenant_schema text PRIMARY KEY,
    revision bigint NOT NULL
);

CREATE OR REPLACE FUNCTION sleep_shadow.bump_rebuild_input_revision()
RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog AS $rebuild$
DECLARE
    before_row jsonb;
    after_row jsonb;
BEGIN
    IF TG_OP IN ('UPDATE', 'DELETE') THEN
        before_row := to_jsonb(OLD);
    END IF;
    IF TG_OP IN ('INSERT', 'UPDATE') THEN
        after_row := to_jsonb(NEW);
    END IF;

    -- Ignore derived projections written by the rebuild itself.
    CASE TG_TABLE_NAME
    WHEN 'daily_scores' THEN
        before_row := before_row - ARRAY['readiness','score_version','computed_at',
            'energy_capacity','energy_eod_current','energy_drain','energy_verdict'];
        after_row := after_row - ARRAY['readiness','score_version','computed_at',
            'energy_capacity','energy_eod_current','energy_drain','energy_verdict'];
    WHEN 'subjective_checkins' THEN
        IF before_row IS NOT NULL THEN
            before_row := jsonb_build_array(before_row->'date',before_row->'source',before_row->'status',before_row->'answer');
        END IF;
        IF after_row IS NOT NULL THEN
            after_row := jsonb_build_array(after_row->'date',after_row->'source',after_row->'status',after_row->'answer');
        END IF;
    WHEN 'settings' THEN
        IF before_row->>'key' = 'timezone' OR before_row->>'key' LIKE 'energy.%' THEN
            before_row := jsonb_build_array(before_row->'key',before_row->'value');
        ELSE
            before_row := NULL;
        END IF;
        IF after_row->>'key' = 'timezone' OR after_row->>'key' LIKE 'energy.%' THEN
            after_row := jsonb_build_array(after_row->'key',after_row->'value');
        ELSE
            after_row := NULL;
        END IF;
    ELSE
        NULL;
    END CASE;

    IF TG_OP <> 'TRUNCATE' AND before_row IS NOT DISTINCT FROM after_row THEN
        RETURN NULL;
    END IF;
    UPDATE sleep_shadow.rebuild_input_revision SET revision = revision + 1
    WHERE tenant_schema = TG_TABLE_SCHEMA;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'missing shadow rebuild input revision for schema %', TG_TABLE_SCHEMA;
    END IF;
    RETURN NULL;
END;
$rebuild$;

REVOKE ALL ON FUNCTION sleep_shadow.bump_rebuild_input_revision() FROM PUBLIC;
