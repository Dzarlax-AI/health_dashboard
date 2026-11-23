-- In one tenant's read-only transaction. Return counts only; never emit report bodies.
with reports as (
    select context from report_history where kind='morning'
), evidence as (
    select e.item from daily_insight_bundles b cross join lateral
        jsonb_array_elements(case when jsonb_typeof(b.snapshot->'evidence')='array'
            then b.snapshot->'evidence' else '[]'::jsonb end) e(item)
), points as (
    select health_record_id,count(*) n from metric_points where metric_name='heart_rate_variability' group by health_record_id
), receipts as (
    select p.n,h.id,h.payload from points p left join health_records h on h.id=p.health_record_id
), scope as (
    select relnamespace from pg_class where oid='metric_points'::regclass
)
select jsonb_build_object(
    'morning_reports',(select count(*) from reports),
    'saved_decisions',(select count(*) from reports where jsonb_typeof(context->'decision')='object'),
    'saved_decision_mode',(select count(*) from reports where context#>>'{decision,mode}' is not null),
    'saved_signal_keys',(select count(*) from reports where jsonb_typeof(context#>'{decision,signal_keys}')='array'),
    'saved_domains',(select count(*) from reports where jsonb_typeof(context#>'{decision,evidence_domains}')='array'),
    'energy_branch_with_archived_action',(select count(*) from reports where context#>'{decision,evidence_domains}'='["energy"]'::jsonb
        and context#>>'{guidance_basis,energy_action}' is not null),
    'archived_energy_action_mismatches',(select count(*) from reports where context#>'{decision,evidence_domains}'='["energy"]'::jsonb
        and context#>>'{guidance_basis,energy_action}' is not null
        and context#>>'{decision,mode}' is distinct from context#>>'{guidance_basis,energy_action}'),
    'complete_decision_packets',(select count(*) from reports where context ?& array['readiness_today','today_guidance','energy_bank','headline','illness_suspicion','sleep_quality']),
    'report_illness_arrays',(select count(*) from reports where jsonb_typeof(context#>'{illness_suspicion,signals}')='array'
        or jsonb_typeof(context#>'{decision,illness_suspicion,signals}')='array'),
    'today_evidence_items',(select count(*) from evidence),
    'today_illness_signal_items',(select count(*) from evidence where item ?& array['metric','role','category','direction','strength','status','contributes']),
    'dashboard_or_today_illness_packets',(select sum(n) from (
        select count(*) n from daily_insight_bundles where jsonb_typeof(snapshot#>'{illness_suspicion,signals}')='array'
        union all select count(*) from dashboard_cache_snapshots where jsonb_typeof(payload#>'{illness_suspicion,signals}')='array') counts),
    'capture_tables',(select count(*) from pg_class t join scope s using(relnamespace)
        where relname in ('captured_inputs','hrv_point','decisions','evidence_signals','band_history')),
    'hrv_method_or_boundary_columns',(select count(*) from pg_attribute where attrelid='metric_points'::regclass
        and attname in ('method','time_kind','start_at','end_at') and not attisdropped),
    'hrv_rows',(select coalesce(sum(n),0) from receipts),
    'hrv_linked_receipts',(select count(id) from receipts),
    'hrv_rows_without_receipt',(select coalesce(sum(n),0) from receipts where id is null),
    'hrv_rows_with_method_marker_in_receipt',(select coalesce(sum(n),0) from receipts
        where payload ~* 'rmssd|sdnn|"(method|statistic|statisticalMethod)"[[:space:]]*:'),
    'hrv_xml_origin',(select count(*) from metric_points where metric_name='heart_rate_variability' and origin='apple_health_xml')
);
