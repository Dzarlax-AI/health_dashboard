-- Admission rules for the episode-specific inputs to the unchanged Go formula.
\set ON_ERROR_STOP on
begin;
do $$ begin
    if current_database()<>'sem_synthetic' then raise exception 'synthetic-only test'; end if;
end $$;
insert into source_data.calendar values ('2001-05-02'),('2001-05-03'),('2001-05-04');
insert into source_data.completed_night_sleep(wake_date,duration_hours,source,source_epoch,algorithm_version,
    input_hash,capture_completeness,duration_assessment,finalization_state,claim_eligibility)
select date,8,'quality-watch','epoch1','qa','fixed','complete','plausible','final','eligible'
from source_data.calendar where date in ('2001-05-02','2001-05-03');
insert into source_data.sleep_interval values
    ('2001-05-02','quality-watch','2001-05-01 22:00Z','2001-05-01 23:00Z','deep'),
    ('2001-05-02','quality-watch','2001-05-01 23:00Z','2001-05-02 00:30Z','rem'),
    ('2001-05-02','quality-watch','2001-05-02 00:30Z','2001-05-02 06:00Z','core');
insert into source_data.sleep_stage_day(date,source,metric_name,observed_minutes) values
    ('2001-05-03','quality-watch','sleep_deep',60),('2001-05-03','quality-watch','sleep_rem',90),
    ('2001-05-03','quality-watch','sleep_core',330),('2001-05-03','quality-watch','sleep_awake',0);
do $$ begin
    if not exists(select from dbt_output.semantic_sleep_night_card_v1 where wake_date='2001-05-02'
       and card->>'quality_awake_basis'='inferred_zero_complete_stages'
       and card->'quality'='{"confidence":"final","duration_pct":100,"continuity_pct":100,"structure_pct":96,"score_pct":96}'::jsonb
       and card #> '{stages,awake_minutes}'='null'::jsonb) then raise exception 'inferred zero provenance'; end if;
    if not exists(select from dbt_output.semantic_sleep_night_card_v1 where wake_date='2001-05-03'
       and card->>'quality_awake_basis'='recorded' and card->>'quality_basis'='matched_day_summary'
       and card#>>'{quality,confidence}'='final') then raise exception 'explicit zero on matched source summary'; end if;
    if not exists(select from dbt_output.semantic_sleep_night_card_v1 where wake_date='2001-05-04'
       and card->'quality'='{"confidence":"missing","duration_pct":0}'::jsonb) then raise exception 'missing night'; end if;
end $$;
insert into source_data.sleep_interval values
    ('2001-05-02','quality-watch','2001-05-02 06:00Z','2001-05-02 06:30Z','awake');
update source_data.completed_night_sleep set finalization_state='provisional',claim_eligibility='ineligible'
where wake_date='2001-05-03';
do $$ begin
    if not exists(select from dbt_output.semantic_sleep_night_card_v1 where wake_date='2001-05-02'
        and card->>'quality_basis'='open_awake_boundary'
        and card->'quality'='{"confidence":"low","duration_pct":100,"continuity_pct":94,"structure_pct":96,"score_pct":91}'::jsonb)
        then raise exception 'low evidence score'; end if;
    if not exists(select from dbt_output.semantic_sleep_night_card_v1 where wake_date='2001-05-03'
        and card->>'quality_basis'='night_not_final_eligible'
        and card->'quality'='{"confidence":"partial","duration_pct":100}'::jsonb)
        then raise exception 'preliminary night gate'; end if;
end $$;
rollback;
