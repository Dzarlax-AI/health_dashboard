{{ config(enabled=var('archived_profile_fields', false), materialized='view',
    post_hook='alter view {{ this }} set (security_invoker=true)') }}

select h.id,h.revision,h.local_date,h.language,h.format_revision,h.prepared_at,
       h.context#>>'{sleep,date}' as sleep_date,s.*
from {{ source('archive_records','report_history') }} h
cross join lateral jsonb_to_record(h.context->'sleep') s(
    analysis_status text,recent_recorded_mean_hours float8,recent_recorded_nights integer,
    recent_recorded_difference_minutes integer,hours float8,baseline_hours float8,baseline_nights integer,
    capture text,assessment text,finalization text
)
where h.kind='morning' and jsonb_typeof(h.context->'sleep')='object'
