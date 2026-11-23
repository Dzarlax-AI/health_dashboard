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
        )
        select * from ({{ proven_step_edits('points','cached',incoming,this,affected_dates_filter('substring(n.date,1,10)'),
                             'true' if var('aggregate_capture_mode','snapshot') == 'snapshot' else 'false',
                             var('tenant_aggregate_worker', false)) }}) edits
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
