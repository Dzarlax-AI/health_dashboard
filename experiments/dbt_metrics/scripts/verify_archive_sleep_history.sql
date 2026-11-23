with dates as (
    select c.date, h::date::text as historical
    from source_data.calendar c cross join lateral generate_series(c.date::date-30,c.date::date-1,interval '1 day') h
), expected as (
    select d.date,d.historical,coalesce((select rule.reason from (values
        (1,n.wake_date is null,'no_current_night'),(2,h.wake_date is null,'no_history_night'),
        (3,n.source<>h.source,'source_changed'),(4,n.source_epoch<>h.source_epoch,'epoch_changed'),
        (5,n.algorithm_version<>h.algorithm_version,'algorithm_changed'),
        (6,h.capture_completeness<>'complete','capture_'||h.capture_completeness),
        (7,h.duration_assessment<>'plausible','duration_'||h.duration_assessment),
        (8,h.finalization_state<>'final','not_final'),(9,h.claim_eligibility<>'eligible','ineligible_claim'))
        rule(priority,excluded,reason) where excluded order by priority limit 1),'eligible') as reason
    from dates d left join source_data.completed_night_sleep n on n.wake_date=d.date
    left join source_data.completed_night_sleep h on h.wake_date=d.historical
)
select json_build_object('rows',count(*),'diff',count(*) filter(where (e.date,e.historical,e.reason)
    is distinct from (v.wake_date,v.history_date,v.reason))) from expected e
full join archive_formula.semantic_sleep_history_v1 v on (v.wake_date,v.history_date)=(e.date,e.historical);
