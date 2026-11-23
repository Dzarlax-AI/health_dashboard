{{ config(enabled=var('complex_sleep_parity',false),materialized='view',post_hook="alter view {{ this }} set (security_invoker = true)") }}

-- The saved hash-change clock is independent of repeat receipts. Older
-- captures lacking that clock fall back to their latest ingestion time.
with clocks as (
  select *, decision_at-candidate_wake as age,
         decision_at-coalesce(quiet_since,latest_ingest) as quiet,
         candidate_wake at time zone zone as local_wake
  from {{ source('complex_sleep','wake_capture') }}
), classified as (
  select *, case
    when candidate_wake is null then 'no_data'
    when age<interval '30 minutes' then 'recent_segment'
    when quiet<interval '20 minutes' then 'still_writing'
    when steps>=100 and signal='detailed_stage_end' then 'post_wake_activity'
    when steps>=100 then 'post_wake_activity_fallback'
    when signal<>'detailed_stage_end' and age<interval '90 minutes' then 'fallback_candidate'
    when typical_minute-(extract(hour from local_wake)*60+extract(minute from local_wake))>120
      then case when decision_at < least(candidate_wake+interval '90 minutes',
                 ((local_wake::date+make_interval(mins=>typical_minute)) at time zone zone)-interval '30 minutes')
           then 'early_candidate' else 'early_candidate_timeout' end
    when age<interval '60 minutes' then 'awaiting_confirmation'
    else 'quiet_timeout' end as reason
  from clocks
)
select profile,capture,candidate_wake,reason,
       reason in ('post_wake_activity','post_wake_activity_fallback','early_candidate_timeout','quiet_timeout') as ready,
       case when reason='post_wake_activity' then 'high'
            when reason in ('post_wake_activity_fallback','early_candidate_timeout','quiet_timeout') then 'medium'
            else 'low' end as confidence
from classified
