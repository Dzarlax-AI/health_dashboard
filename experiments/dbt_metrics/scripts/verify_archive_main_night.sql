with expected as (
    select c.date as wake_date,n.source,n.source_epoch,n.algorithm_version,n.input_hash,n.duration_hours,
       n.duration_assessment,n.capture_completeness,n.finalization_state,n.claim_eligibility
    from source_data.calendar c left join source_data.completed_night_sleep n on n.wake_date=c.date
), compared as (
    select e.source is not null as recorded,m.status='no_night' as missing,
           (e.wake_date,e.source,e.source_epoch,e.algorithm_version,e.input_hash,e.duration_hours,
            e.duration_assessment,e.capture_completeness,e.finalization_state,e.claim_eligibility)
             is distinct from
           (m.wake_date,m.source,m.source_epoch,m.algorithm_version,m.input_hash,m.duration_hours,
            m.duration_assessment,m.capture_completeness,m.finalization_state,m.claim_eligibility) as differs
    from expected e full join archive_formula.semantic_sleep_day_v1 m using(wake_date)
)
select json_build_object('calendar_days',count(*),'recorded',count(*) filter(where recorded),
    'missing',count(*) filter(where missing),'differences',count(*) filter(where differs or recorded=missing))
from compared;
