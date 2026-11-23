{{ config(enabled=var('briefing_components_shadow', false), materialized='view',
    post_hook='alter view {{ this }} set (security_invoker=true)') }}

-- computeDashboardSleepQuality reads four component states in order and stops
-- at the first missing one. The factor formula is shared with the night card.
with recursive captures as (
    select profile,capture,inputs#>'{raw,ReadinessEvidence}' as evidence
    from ({{ today_metric_captures_v1() }}) c
    where jsonb_typeof(inputs->'raw')='object'
), folded as (
    select profile,capture,evidence,0 as step,false as stopped,
           coalesce((evidence#>>'{SleepQuality,present}')::boolean,false) as evidence_present,
           coalesce(evidence#>>'{SleepQuality,freshness}','') as evidence_freshness,
           coalesce(evidence#>>'{SleepQuality,confidence}','') as evidence_confidence
    from captures
    union all
    select f.profile,f.capture,f.evidence,f.step+1,i.absent,
           f.evidence_present and not i.absent,
           case when i.absent then 'missing' when i.stale then 'stale' else f.evidence_freshness end,
           case when i.absent then f.evidence_confidence when i.low then 'low'
                when i.stale then 'provisional' else f.evidence_confidence end
    from folded f cross join lateral (
        select f.evidence -> (array['SleepDuration','SleepDeep','SleepREM','SleepAwake'])[f.step+1] as v
    ) part cross join lateral (
        select coalesce((v->>'present')::boolean,false)=false or v->>'value' is null as absent,
               v->>'freshness'='stale' as stale,v->>'confidence'='low' as low
    ) i where not f.stopped and f.step<4
), inputs as (
    select profile,capture,evidence_present,evidence_freshness,evidence_confidence,
           (evidence#>>'{SleepDuration,value}')::float8 as total_hours,
           (evidence#>>'{SleepDeep,value}')::float8 as deep_hours,
           (evidence#>>'{SleepREM,value}')::float8 as rem_hours,
           (evidence#>>'{SleepAwake,value}')::float8 as awake_hours
    from folded where stopped or step=4
), calculated as ({{ semantic_sleep_quality('inputs') }})
select profile,capture,quality,(quality->>'score_pct')::int as score_pct,
       (quality->>'duration_pct')::int as duration_pct,(quality->>'continuity_pct')::int as continuity_pct,
       (quality->>'structure_pct')::int as structure_pct,confidence
from calculated
