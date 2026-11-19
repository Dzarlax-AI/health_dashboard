{% macro shadow_sleep_daily_v1(candidates, scope) %}
WITH ranked AS (
    SELECT profile, wake_date, id AS candidate, revision, snapshot,
           row_number() OVER (PARTITION BY profile, wake_date ORDER BY priority, id) AS rank
    FROM {{ candidates }}
    WHERE {{ scope }}
)
SELECT w.profile, w.wake_date, w.candidate, w.revision,
       observed.sleep_seconds, w.snapshot->'stages' AS stages,
       observed.deep_seconds, observed.rem_seconds, observed.core_seconds,
       observed.unspecified_seconds, observed.awake_seconds, observed.stage_conflict
FROM ranked w
CROSS JOIN LATERAL (
    WITH intervals AS (
        SELECT kind, tstzrange(start_at, end_at, '[)') AS span
        FROM jsonb_to_recordset(w.snapshot->'stages')
            AS stage(kind text, start_at timestamptz, end_at timestamptz)
    ), ranges AS (
        SELECT range_agg(span) FILTER (WHERE kind IN ('deep','rem','core','asleep_unspecified')) AS asleep,
               range_agg(span) FILTER (WHERE kind IN ('deep','rem','core')) AS detailed,
               range_agg(span) FILTER (WHERE kind='deep') AS deep,
               range_agg(span) FILTER (WHERE kind='rem') AS rem,
               range_agg(span) FILTER (WHERE kind='core') AS core,
               range_agg(span) FILTER (WHERE kind='asleep_unspecified') AS unspecified,
               range_agg(span) FILTER (WHERE kind='awake') AS awake
        FROM intervals
    ), checked AS (
        SELECT *,
               coalesce(deep && rem, false) OR coalesce(deep && core, false)
                   OR coalesce(rem && core, false) OR coalesce(asleep && awake, false) AS conflict
        FROM ranges
    ), seconds AS (
        SELECT phase.kind, conflict,
               CASE WHEN phase.kind<>'total' AND conflict THEN NULL
                    WHEN phase.spans IS NOT NULL THEN coalesce(d.seconds,0)
                    WHEN w.snapshot->'zero_stages' ? phase.kind THEN 0
               END AS value
        FROM checked
        CROSS JOIN LATERAL (VALUES
            ('total',asleep), ('deep',deep), ('rem',rem), ('core',core),
            ('asleep_unspecified',unspecified-coalesce(detailed,'{}'::tstzmultirange)), ('awake',awake)
        ) phase(kind,spans)
        CROSS JOIN LATERAL (
            SELECT sum(extract(epoch FROM upper(span)-lower(span))) AS seconds
            FROM unnest(phase.spans) span
        ) d
    )
    SELECT max(value) FILTER (WHERE kind='total')::double precision AS sleep_seconds,
           max(value) FILTER (WHERE kind='deep')::double precision AS deep_seconds,
           max(value) FILTER (WHERE kind='rem')::double precision AS rem_seconds,
           max(value) FILTER (WHERE kind='core')::double precision AS core_seconds,
           max(value) FILTER (WHERE kind='asleep_unspecified')::double precision AS unspecified_seconds,
           max(value) FILTER (WHERE kind='awake')::double precision AS awake_seconds,
           bool_or(conflict) AS stage_conflict
    FROM seconds
) observed
WHERE w.rank=1
{% endmacro %}
