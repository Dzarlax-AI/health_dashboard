{% macro completed_night_sleep_v1(inputs) %}
with normalized as (
    select i.*,
           case when persisted_state then coalesce(persisted_capture_completeness,'unknown')
                when capture_completeness='partial' then 'partial'
                when capture_completeness='complete' and coalesce(coverage_generation,'')<>''
                 and covered_interval_start is not null and covered_interval_end>covered_interval_start then 'complete'
                else 'unknown' end as capture_state,
           ((wake_date::date + time '18:00') at time zone zone) as local_finalization_at
    from {{ inputs }} i
), assessed as (
    select *, case when persisted_state then coalesce(persisted_duration_assessment,'unknown')
                   else {{ completed_night_duration_assessment_v1('duration_hours') }} end as duration_assessment,
           (decision_at >= local_finalization_at) as is_final
    from normalized
)
select *, case when is_final then 'final' else 'provisional' end as finalization_state,
       case when capture_state='complete' and duration_assessment='plausible' and is_final
            then 'eligible' else 'ineligible' end as claim_eligibility,
       case when is_final then coalesce(existing_finalized_at, finalized_at_fallback, local_finalization_at) end as finalized_at
from assessed
{% endmacro %}
