{% macro semantic_sleep_quality(inputs) %}
-- The arguments to ComputeSleepQualityBreakdown are unrounded hours and a
-- ReadinessComponentEvidence. The caller owns their provenance and confidence.
with factors as (
    select i.*,
           least(1.0::double precision,total_hours / 8.0) as duration_factor,
           total_hours / nullif(total_hours + awake_hours,0) as continuity_factor,
           greatest(0.5::double precision, 1.0
               - greatest(0.0,0.15 - deep_hours / nullif(total_hours,0))
               - greatest(0.0,0.20 - rem_hours / nullif(total_hours,0))) as structure_factor,
           case when total_hours is null or total_hours<=0 then 'missing'
                when deep_hours is null or rem_hours is null or awake_hours is null then 'partial'
                when evidence_confidence='low' or evidence_freshness='stale' then 'low'
                when evidence_present and evidence_freshness='ok' and evidence_confidence='final' then 'final'
                else 'partial' end as confidence
    from {{ inputs }} i
)
select f.*, scores.components || jsonb_build_object('confidence',f.confidence) as quality
from factors f cross join lateral (
    select jsonb_object_agg(key,case when pct >= 'Infinity'::double precision or pct<=0 or pct is null then 0
                                    when pct>=100 then 100
                                    else floor(pct)::int + ((pct-floor(pct))>=0.5)::int end) as components
    from (values
        ('duration_pct',case when f.confidence<>'missing' then f.duration_factor*100 else 0 end),
        ('continuity_pct',f.continuity_factor*100),
        ('structure_pct',f.structure_factor*100),
        ('score_pct',f.duration_factor*f.continuity_factor*f.structure_factor*100)
    ) v(key,pct)
    where key='duration_pct' or f.confidence in ('final','low')
) scores
{% endmacro %}
