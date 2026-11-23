{% macro prepare_step_replacements(after_merge=false) %}
{% if is_incremental() %}
    {% set state = adapter.get_relation(database=this.database,schema=this.schema,identifier=('hdbt_w_step' if var('tenant_aggregate_worker', false) else 'step_hour_state')) %}
    {% set old_columns = adapter.get_columns_in_relation(this) | map(attribute='name') | list %}
    {% if state is not none and 'source_snapshot_at' in old_columns and 'proven_input' in (adapter.get_columns_in_relation(state) | map(attribute='name') | list) %}
        {% set incoming = source('source_data','metric_point_changes' if var('aggregate_capture_mode','snapshot') == 'partial' else 'metric_points') %}
        {% if not after_merge %}
        create temporary table captured_step_edits on commit drop as
        with points as (
            {{ step_point_inputs(this,source('source_data','health_records'),affected_dates_filter('substring(p.date,1,10)')) }}
        ), cached as (
            select * from {{ state }} where avg_val is not null and {{ affected_dates_filter('substring(hour,1,10)') }}
        ), signatures as ({{ step_capture_signature('points') }}), unproven as (
            select c.* from cached c where not exists(select from signatures t where (c.hour,c.proven_input)=(t.hour,t.signature))
        ), frozen as ({{ incomplete_step_hours('points','unproven', ordered_steps=var('tenant_aggregate_worker', false)) }}),
        edits as (
            select distinct substring(n.date,1,13)||':00' as hour
            from {{ incoming }} n left join {{ this }} old using(metric_name,date,source)
            where n.metric_name in ('step_count','__healthkit_archive_sample:step_count') and {{ affected_dates_filter('substring(n.date,1,10)') }}
              and (n.qty,n.quality,n.origin,n.health_record_id,n.units,n.source_snapshot_at,n.received_at,n.id) is distinct from
                  (old.qty,old.quality,old.origin,old.health_record_id,old.units,old.source_snapshot_at,old.received_at,old.id)
              and n.units='count' and n.quality='ok' and n.qty>=0
        )
        select e.hour from edits e where not exists(select from frozen f where f.hour=e.hour)
        {% if var('aggregate_capture_mode','snapshot') == 'snapshot' %}
          and not exists (
            select from {{ this }} old where old.metric_name in ('step_count','__healthkit_archive_sample:step_count')
              and substring(old.date,1,13)||':00'=e.hour
              and not exists(select from {{ incoming }} n where (n.metric_name,n.date,n.source)=(old.metric_name,old.date,old.source))
          )
        {% endif %}
        {% else %}
        -- A failed later model retains its previous numeric state. A retry can
        -- consume the proof only while the exact retained post-image matches.
        with points as (
            {{ step_point_inputs(this,source('source_data','health_records'),"substring(p.date,1,13)||':00' in (select hour from captured_step_edits)") }}
        ), signatures as ({{ step_capture_signature('points') }})
        update {{ state }} s set proven_input=t.signature from signatures t where s.hour=t.hour
        {% endif %}
    {% endif %}
{% endif %}
{% endmacro %}
