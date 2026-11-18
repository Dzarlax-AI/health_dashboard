{% macro sleep_duration_reference_v1(nights) %}
select
    current_night.wake_date,
    current_night.source,
    current_night.source_epoch,
    current_night.algorithm_version,
    to_char((current_night.wake_date::date - 30)::timestamp, 'YYYY-MM-DD') as window_start,
    current_night.wake_date as window_end_exclusive,
    history.eligible_nights,
    history.reference_hours,
    'sleep_duration_reference_v1'::text as rule_version,
    history.history_version,
    history.eligible_dates
from {{ nights }} as current_night
cross join lateral (
    select
        count(*)::integer as eligible_nights,
        case when count(*) >= 7 then avg(previous.duration_hours) end as reference_hours,
        encode(sha256(convert_to(coalesce(jsonb_agg(
            jsonb_build_array(previous.wake_date, previous.duration_hours, previous.input_hash,
                previous.capture_completeness, previous.duration_assessment,
                previous.finalization_state, previous.claim_eligibility)
            order by previous.wake_date
        )::text, '[]'), 'UTF8')), 'hex') as history_version,
        coalesce(array_agg(previous.wake_date order by previous.wake_date), array[]::text[]) as eligible_dates
    from {{ nights }} as previous
    where previous.wake_date >= to_char((current_night.wake_date::date - 30)::timestamp, 'YYYY-MM-DD')
      and previous.wake_date < current_night.wake_date
      and previous.source = current_night.source
      and previous.source_epoch = current_night.source_epoch
      and previous.algorithm_version = current_night.algorithm_version
      and previous.capture_completeness = 'complete'
      and previous.duration_assessment = 'plausible'
      and previous.finalization_state = 'final'
      and previous.claim_eligibility = 'eligible'
) as history
{% endmacro %}
