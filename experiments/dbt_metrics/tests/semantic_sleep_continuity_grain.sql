{{ config(enabled=var('semantic_shadow', false)) }}
select c.date from {{ source('source_data','calendar') }} c
left join {{ ref('semantic_sleep_continuity_v1') }} v on v.wake_date=c.date
group by c.date having count(v.wake_date) <> 1
union all
select wake_date from {{ ref('semantic_sleep_continuity_v1') }}
where (stage_basis='unavailable') <> (recorded_asleep_minutes is null)
   or (awakening_count is not null) <> (continuity_reason='continuous_recording')
   or (stage_basis='same_source_day_summary' and (episode_start is not null or gap_minutes is not null))
   or least(deep_fraction,rem_fraction,core_fraction,unspecified_fraction) < 0
   or greatest(deep_fraction,rem_fraction,core_fraction,unspecified_fraction) > 1
