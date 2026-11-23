{% macro readiness_score_v1(captures, materialization="") %}
with stats as (
    select c.*, r.n as rhr_n, r.today as rhr_today,r.recent as rhr_recent,
           r.baseline as rhr_base,r.sd as rhr_sd,
           s.n as sleep_n,s.today as sleep_today,s.recent as sleep_recent,
           s.baseline as sleep_base,s.sd as sleep_sd,s.recent_sd as sleep_recent_sd
    from {{ captures }} c
    cross join lateral (
        select count(*) as n,max(v) filter(where pos=1) as today,
               avg(v) filter(where pos<=7) as recent,stddev_pop(v) filter(where pos<=7) as recent_sd,
               avg(v) filter(where pos>7) as baseline,stddev_pop(v) filter(where pos>7) as sd
        from unnest(c.rhr) with ordinality as a(v,pos)
    ) r
    cross join lateral (
        select count(*) as n,max(v) filter(where pos=1) as today,
               avg(v) filter(where pos<=7) as recent,stddev_pop(v) filter(where pos<=7) as recent_sd,
               avg(v) filter(where pos>7) as baseline,stddev_pop(v) filter(where pos>7) as sd
        from unnest(c.sleep) with ordinality as a(v,pos)
    ) s
), contributions as (
    select *,
        case when rhr_n>=9 and (reader='history' or rhr_present) then
            case when rhr_sd<1e-9 then 0 else
                -0.6*((rhr_today-rhr_base)/rhr_sd)-0.4*((rhr_recent-rhr_base)/rhr_sd) end
        end as rhr_z,
        case when sleep_n>=9 and (reader='history' or sleep_present) then
            case when sleep_sd<1e-9 then 0 else
                0.6*((sleep_today-sleep_base)/sleep_sd)+0.4*((sleep_recent-sleep_base)/sleep_sd) end
            + case when sleep_recent<5 then -1.5 when sleep_recent<5.5 then -1
                   when sleep_recent<6 then -0.5 when sleep_recent>=10 then -1
                   when sleep_recent>=9.5 then -0.5 else 0 end
            + case when sleep_recent<0.1 then 0 when sleep_recent_sd/sleep_recent*100>25 then -0.5
                   when sleep_recent_sd/sleep_recent*100>15 then -0.25 else 0 end
        end as sleep_z
    from stats
), weighted as {{ materialization }} (
    select *,coalesce(hrv_weighted_z,0)+coalesce(rhr_z*0.25,0)+coalesce(sleep_z*0.35,0) as numerator,
           coalesce(hrv_weight,0)+(case when rhr_z is null then 0 else .25 end)
               +(case when sleep_z is null then 0 else .35 end) as denominator
    from contributions
), raw as {{ materialization }} (
    select *,floor(least(100,greatest(0,70+15*coalesce(numerator/nullif(denominator,0),0)))+0.5)::int as raw_score,
       case when reader='morning' then
         case when not hrv_present or not rhr_present or not sleep_present then 'missing_same_day_evidence'
              when hrv_provisional then 'hrv_provisional' when hrv_samples between 1 and 3 then 'hrv_sparse' end end as evidence_reason
    from weighted
), evidence_cap as (
    select *, case when evidence_reason is null then raw_score else least(raw_score,65) end as after_evidence
    from raw
), quality_cap as (
    select *,case when reader='morning' and low_sleep_quality
                  then least(65,greatest(0,after_evidence-10)) else after_evidence end as after_quality
    from evidence_cap
), reasons as (
    select *,case when reader='morning' then
      case when illness='high' then 'illness_suspicion_high'
           when illness='moderate' then 'illness_suspicion_moderate'
           when evidence_reason is not null then evidence_reason
           when low_sleep_quality then 'sleep_quality_low'
           when after_quality>65 and headline in ('stress','sleep_debt') then headline||'_headline' end
      end as cap_reason
    from quality_cap
), result as (
    select *,case when cap_reason='illness_suspicion_high' then least(45,after_quality)
                  when cap_reason is not null then least(65,after_quality) else after_quality end as display_score
    from reasons
)
select case_id,profile,target_date,reader,
       case when not coalesce(captured,false) then coalesce(hrv_reason,'missing_capture')
            when denominator=0 then 'neutral_no_components' else 'calculated' end as status,
       case when captured then hrv_weighted_z end as hrv_contribution,
       case when captured then rhr_z*0.25 end as rhr_contribution,
       case when captured then sleep_z*0.35 end as sleep_contribution,
       hrv_reason as hrv_skip_reason,
       case when captured and reader='morning' and not rhr_present then 'missing_same_day_evidence'
            when captured and rhr_n<9 then 'insufficient_observations' end as rhr_skip_reason,
       case when captured and reader='morning' and not sleep_present then 'missing_same_day_evidence'
            when captured and sleep_n<9 then 'insufficient_observations' end as sleep_skip_reason,
       case when captured then numerator end as weighted_sum,
       case when captured then denominator end as available_weight,
       case when captured then raw_score end as raw_score,
       case when captured then display_score end as display_score,
       case when captured then case when display_score>=80 then 'optimal' when display_score>=50 then 'fair' else 'low' end end as band,
       case when captured and reader='morning' then case when illness='high' then 'low'
           when cap_reason is not null then 'provisional' else 'final' end end as confidence,
       case when captured then cap_reason end as cap_reason
from result
{% endmacro %}
