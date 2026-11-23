{{ config(enabled=var('archived_profile_fields', false), materialized='view',
    post_hook='alter view {{ this }} set (security_invoker=true)') }}

select b.date, b.lang, b.decision_id, b.schema_version, item.ordinality, e.*
from {{ source('archive_records', 'daily_insight_bundles') }} b
cross join lateral jsonb_array_elements(nullif(b.snapshot->'evidence','null'::jsonb))
    with ordinality as item(payload, ordinality)
cross join lateral jsonb_to_record(item.payload) as e(
    id text, domain text,
    observed_at timestamptz,
    comparison_period text,
    comparison text,
    data_state text,
    confidence text,
    value double precision,
    baseline double precision,
    delta double precision
)
