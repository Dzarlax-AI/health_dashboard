{{ config(alias='hdbt_w_points' if var('tenant_aggregate_worker', false) else 'retained_metric_points', materialized='incremental', unique_key=('id' if var('tenant_aggregate_worker', false) else ['metric_name','date','source']), incremental_strategy='merge', on_schema_change='append_new_columns',
          pre_hook=["{{ prepare_step_replacements() }}","{{ prepare_metric_capture() }}"],post_hook="{{ prepare_step_replacements(true) }}") }}
{% do validate_affected_dates() %}
{% set mode = var('aggregate_capture_mode', 'snapshot') %}
{% if mode not in ['snapshot','partial'] %}
    {{ exceptions.raise_compiler_error('aggregate_capture_mode must be snapshot or partial') }}
{% endif %}
{% if execute and mode == 'partial' and not is_incremental() %}
    {{ exceptions.raise_compiler_error('partial captures require a retained complete snapshot; missing earlier points cannot be reconstructed') }}
{% endif %}
-- The live server already retains exact accepted points. This local adapter
-- lets offline change batches feed the same complete per-hour inputs.
select p.metric_name,p.date,p.source,p.qty,p.quality,p.health_record_id,p.origin,p.units,p.id,p.received_at,p.source_snapshot_at
from {{ source('source_data', 'metric_point_changes' if mode == 'partial' else 'metric_points') }} p
{% if is_incremental() %} where {{ affected_dates_filter('substring(date,1,10)') }} {% endif %}
