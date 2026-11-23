{% macro energy_bank_v3(runs, days) %}
-- EnergyBank v2, formula revision 3. Each capture freezes the effective daily
-- inputs and coefficient settings of one computation. Later corrections have
-- their own capture and cannot rewrite an observed intraday bucket.
with recursive calendar as (
    select c.*, n as i, c.target_day - (20 - n) as day
    from {{ runs }} c
    cross join generate_series(0,20) n
), raw as (
    select c.*, d.sleep_total, d.active_kcal, d.hr_load, d.stress_flags,
           case when d.sleep_total > 0 and d.deep is not null and d.rem is not null
                     and d.awake is not null then
                least(1.0, d.sleep_total/8.0) * (d.sleep_total/(d.sleep_total+d.awake))
                * greatest(0.5, 1.0 - greatest(0.0,0.15-d.deep/d.sleep_total)
                                     - greatest(0.0,0.20-d.rem/d.sleep_total))
           end as sq,
           case when d.active_kcal >= 0 then
                case when d.active_kcal > 0 and d.active_kcal < 'Infinity'::float8
                           and c.alpha*c.factor > 0 and c.alpha*c.factor < 'Infinity'::float8
                     then d.active_kcal*(c.alpha*c.factor) else 0.0 end
                + case when c.stress_enabled and c.beta > 0 and c.beta < 'Infinity'::float8
                            and d.hr_load > 0 and d.hr_load < 'Infinity'::float8
                       then d.hr_load*c.beta else 0.0 end
           end as drain
    from calendar c left join {{ days }} d
      using (profile, capture_id, day)
), filled as (
    select d.*, coalesce(d.sq,h.sleep_fallback,0) as effective_sq,
           coalesce(d.drain,h.drain_fallback,0) as effective_drain
    from raw d
    cross join lateral (
        select case when count(p.sq) filter(where p.i >= d.i-7) >= 3
                    then avg(p.sq order by p.i) filter(where p.i >= d.i-7)
                    else percentile_cont(.5) within group(order by p.sq) end as sleep_fallback,
               case when count(p.drain) filter(where p.i >= d.i-7) >= 3
                    then avg(p.drain order by p.i) filter(where p.i >= d.i-7)
                    else percentile_cont(.5) within group(order by p.drain) end as drain_fallback
        from raw p where p.profile=d.profile and p.capture_id=d.capture_id and p.i<d.i
    ) h
    where d.i>=7
), steps(profile, capture_id, i, bank, restore, incoming, before_clamp) as (
    select profile,capture_id,6,50::float8,0::float8,50::float8,50::float8 from {{ runs }}
    union all
    select b.profile,b.capture_id,d.i,
           greatest(-50.0,least(100.0,x.capacity-d.effective_drain)),
           x.capacity-b.bank,b.bank,x.capacity-d.effective_drain
    from steps b inner join filled d on b.profile=d.profile and b.capture_id=d.capture_id and d.i=b.i+1
    cross join lateral (select b.bank+(100.0-b.bank)*d.effective_sq as capacity) x
), coverage as (
    select profile,capture_id,
           count(*) filter(where sq is null or drain is null) as gap_count,
           20-coalesce(max(i) filter(where sq is not null and drain is not null),6) as trailing_gaps
    from filled group by profile,capture_id
), result as (
    select d.*,b.bank,b.restore,b.incoming,b.before_clamp,
           case when g.trailing_gaps>=5 or g.gap_count>7 then 'stale'
                when g.gap_count>=2 then 'estimated' else 'fresh' end as state,
           array_remove(array[case when d.sq is null then 'imputed_sleep' end,
                              case when d.drain is null then 'imputed_activity' end],null)
               || coalesce(d.stress_flags,'{}'::text[]) as flags
    from filled d join steps b using (profile,capture_id,i)
    join coverage g using (profile,capture_id) where i=20
), rounded as (
    -- Go math.Round ties away from zero, including a signed balance.
    select *, (sign(bank)*floor(abs(bank)) + case when abs(bank)-floor(abs(bank))>=0.5 then sign(bank) else 0 end)::int as signed_bank
    from result
)
select profile,capture_id,target_day,snapshot_kind,state,flags,formula_version,
       alpha*factor as alpha_used,bank as unrounded_bank,signed_bank,
       greatest(0,signed_bank) as display_bank,
       case when state<>'stale' then greatest(0,signed_bank) end as visible_bank,
       case when state='stale' then 0 else
         (floor(effective_drain) + case when effective_drain-floor(effective_drain)>=0.5 then 1 else 0 end)::int end as drain_delta,
       case when state='stale' then 0 else
         (sign(restore)*(floor(abs(restore)) + case when abs(restore)-floor(abs(restore))>=0.5 then 1 else 0 end))::int end as restore_delta,
       case when state<>'stale' then jsonb_strip_nulls(jsonb_build_object(
           'sleep_quality', effective_sq, 'drain',effective_drain,'sleep_total_h',sleep_total,
           'active_kcal',active_kcal,'sustained_hr_load_z',hr_load)) end as components,
       case when snapshot_kind='retrospective_eod' and state='stale' then null
            when snapshot_kind='retrospective_eod' then array_append(flags,'backfilled')
            when state='stale' then array_append(flags,'energy_stale')
            else flags end as persisted_flags,
       effective_sq as effective_quality, restore as unrounded_restore, incoming, before_clamp
from rounded
{% endmacro %}
