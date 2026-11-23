-- Independent endpoint sweep over each selected candidate. Each adjacent
-- pair of timestamps contributes once for each active stage kind.
with winners as materialized (
    select distinct on (profile,wake_date) profile,wake_date,id,revision,snapshot
    from source_data.sleep_rollout_candidates order by profile,wake_date,priority,id
), segments as materialized (
    select w.id,q.kind,q.start_at,q.end_at from winners w cross join lateral
    jsonb_to_recordset(w.snapshot->'stages') q(kind text,start_at timestamptz,end_at timestamptz)
), boundaries as (
    select id,start_at as ts from segments union select id,end_at from segments
), chunks as (
    select id,ts,lead(ts) over(partition by id order by ts) as until from boundaries
), evidence as (
    select c.id,c.ts,extract(epoch from c.until-c.ts) as seconds,array_agg(distinct s.kind) as kinds
    from chunks c join segments s on s.id=c.id and s.start_at<=c.ts and s.end_at>=c.until
    group by c.id,c.ts,c.until
), flags as (
    select *,kinds && array['deep','rem','core','asleep_unspecified'] as asleep,
        kinds && array['deep','rem','core'] as detailed,
        (('deep'=any(kinds))::int+('rem'=any(kinds))::int+('core'=any(kinds))::int)>1
          or ('awake'=any(kinds) and kinds && array['deep','rem','core','asleep_unspecified']) as conflict
    from evidence
), totals as (
    select id, sum(seconds) filter(where asleep) as duration,bool_or(conflict) as conflict,
       sum(seconds) filter(where 'deep'=any(kinds)) as deep,
       sum(seconds) filter(where 'rem'=any(kinds)) as rem,
       sum(seconds) filter(where 'core'=any(kinds)) as core,
       case when bool_or('asleep_unspecified'=any(kinds)) then coalesce(sum(seconds)
         filter(where 'asleep_unspecified'=any(kinds) and not detailed),0) end as unspecified,
       sum(seconds) filter(where 'awake'=any(kinds)) as awake from flags group by id
), outputs as (
    select w.wake_date,w.id,w.revision,w.snapshot->'zero_stages' as zero,coalesce(t.conflict,false) as conflict,
        t.duration,t.deep,t.rem,t.core,t.unspecified,t.awake
    from winners w left join totals t on w.id=t.id
), joined as (
    select e.id,m.candidate,e.conflict,m.stage_conflict,e.zero,
        case when e.duration is not null then e.duration when e.zero ? 'total' then 0 end as duration,
        m.sleep_seconds,e.deep,e.rem,e.core,e.awake,e.unspecified,
        m.deep_seconds,m.rem_seconds,m.core_seconds,m.awake_seconds,m.unspecified_seconds,
        (e.id,e.revision) is distinct from (m.candidate,m.revision) as wrong_key
    from outputs e full join archive_formula.shadow_sleep_daily_v1 m using(wake_date)
), rows as (
    select id,candidate,wrong_key,measurement.* from joined cross join lateral (values
        ('shadow_sleep_daily_duration_v1',duration is distinct from sleep_seconds),
        ('shadow_sleep_daily_stage_conflict_v1',conflict is distinct from stage_conflict)
    ) measurement(metric,diff)
    union all
    select id,candidate,wrong_key,'shadow_sleep_daily_stages_v1',s.actual is distinct from
        (case when not conflict then coalesce(s.value,case when zero ? s.kind then 0 end) end)
    from joined cross join lateral (values ('deep',deep,deep_seconds),('rem',rem,rem_seconds),('core',core,core_seconds),
        ('awake',awake,awake_seconds),('asleep_unspecified',unspecified,unspecified_seconds)) s(kind,value,actual)
), ids as (select unnest(array['shadow_sleep_daily_duration_v1','shadow_sleep_daily_stage_conflict_v1','shadow_sleep_daily_stages_v1']) as metric)
select json_build_array(ids.metric,count(r.id),count(r.candidate),count(*) filter(where r.wrong_key or r.diff))
from ids left join rows r using(metric) group by ids.metric order by ids.metric;
