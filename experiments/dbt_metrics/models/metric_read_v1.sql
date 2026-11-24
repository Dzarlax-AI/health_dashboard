{{ config(enabled=var('profile_metric_reads', false), materialized='view', alias='tenant_native_metric_read_v1' if var('tenant_native_catalog', false) else 'metric_read_v1',
          grants={'select': var('profile_metric_read_roles', [])},
          post_hook=["alter view {{ this }} set (security_invoker=true)"] + (["create or replace view {{ adapter.quote(target.schema) }}.tenant_native_sleep_goal_v1 with (security_invoker=true) as {{ archived_sleep_goal_rows() }}",
                     "create or replace view {{ adapter.quote(target.schema) }}.tenant_native_energy_snapshot_v1 with (security_invoker=true) as {{ archived_energy_snapshot_rows() }}",
                     "create or replace view {{ adapter.quote(target.schema) }}.tenant_native_workout_v1 with (security_invoker=true) as {{ archived_workout_rows(true) }}"] if var('tenant_native_catalog', false) else [])) }}

{% set native = var('tenant_native_catalog', false) %}
{% if native and (var('semantic_source_schema') != target.schema or var('archive_records_schema') != target.schema or var('tenant_aggregate_worker', false)) %}
    {{ exceptions.raise_compiler_error("native catalog requires tenant-local sources") }}
{% endif %}
{% set models = var('profile_graph_models', []) %}
{% set observation_ids = var('profile_graph_observations', []) %}
{% if native and (not models or not observation_ids) %}
    {{ exceptions.raise_compiler_error("native catalog requires explicit producer bindings") }}
{% endif %}
{% if models or observation_ids %}
{% set points = source('source_data', 'metric_points') %}
{% set raw_source = (points.schema ~ '.' ~ points.identifier) | replace("'", "''") %}
{% set model_names = models | map(attribute='model') | list %}
with non_values as (
    select key as metric_id, value->>0 as disposition, value->>1 as reason
    from jsonb_each('{{ tojson(var("profile_graph_non_values")) | replace("'","''") }}'::jsonb)
), observation_ids as (
    select jsonb_array_elements_text('{{ tojson(observation_ids) | replace("'","''") }}'::jsonb) as metric_id
), native_sources as (
    select jsonb_array_elements_text('{{ tojson(model_names) | replace("'","''") }}'::jsonb) as name
), definitions as (
    select c.metric_id, c.definition || jsonb_build_object('metric_id',c.metric_id) as definition,
           coalesce(n.disposition,'active') as disposition, n.reason,
           {% if native %}
           case when o.metric_id is not null then jsonb_build_array('{{ raw_source }}'::text)
                when nsrc.name='archived_wake_time_v1' then jsonb_build_array(nsrc.name,'wake_confirmation_v1'::text)
                when nsrc.name='archived_features_v1' then jsonb_build_array(nsrc.name,'recorded_feature_decision_v1'::text,'native_feature_replay_v1'::text)
                when nsrc.name is null then '[]'::jsonb else jsonb_build_array(nsrc.name) end as read_sources,
           {% else %}
           coalesce(d.read_sources,'[]'::jsonb) ||
               case when o.metric_id is not null and not coalesce(d.read_sources ? '{{ raw_source }}', false)
                    then jsonb_build_array('{{ raw_source }}'::text) else '[]'::jsonb end ||
               case when nsrc.name is null or d.read_sources ? nsrc.name then '[]'::jsonb
                    else jsonb_build_array(c.dbt_binding->>'model') end as read_sources,
           {% endif %}
           c.dbt_binding, nsrc.name as native_source
    from {{ ref('semantic_metric_catalog_v1') }} c
    left join non_values n using (metric_id)
    left join observation_ids o using (metric_id)
    left join native_sources nsrc on nsrc.name=c.dbt_binding->>'model'
    {% if native %}where (n.metric_id is not null or o.metric_id is not null or c.metric_id in
        (select jsonb_array_elements_text('{{ tojson(var("native_catalog_bindings")) | replace("'","''") }}'::jsonb)))
      and exists(select from {{ adapter.quote(target.schema) }}.settings where key='dbt_native_catalog_reads_v1' and value='true')
    {% else %}left join {{ source('profile_metrics','metric_definition_v1') }} d using (metric_id){% endif %}
), {% for group in models %}
{{ adapter.quote('producer_' ~ loop.index) }} as {% if group.model == 'daily_metrics' or group.model.startswith('archived_') %}not {% endif %}materialized (
    {% if native and group.model == 'daily_metrics' %}
    select d.* from {{ adapter.quote(target.schema) }}.hdbt_daily_metrics d
    where exists (
        select from {{ adapter.quote(target.schema) }}.hdbt_state s
        join {{ adapter.quote(target.schema) }}.settings j on j.key='_cache_maintenance_v1'
        where s.singleton and s.shadow_enabled and s.initialized and s.ready and s.formula_id='aggregate_serving_v3'
          and s.source_generation=(j.value::jsonb->>'generation')::numeric
          and not exists(select from {{ adapter.quote(target.schema) }}.hdbt_dirty)
          and not exists(select from {{ adapter.quote(target.schema) }}.hdbt_backfill)
    )
    {% elif native and group.model == 'sleep_duration_reference_v1' %}
    select * from {{ adapter.quote(target.schema) }}.sleep_duration_reference_v1
    where exists (select from {{ adapter.quote(target.schema) }}.settings where key='sleep_duration_comparison_enabled' and value='true')
    {% elif native and group.published_decision %}{{ native_published_decisions(group.model) }}
    {% else %}select * from {{ ref(group.model) }}{% endif %}
),
{% endfor %}
facts as (
    {% if not native %}
    select v.metric_id, v.source, v.sample_key, v.value, v.source_metadata
    from {{ source('profile_metrics','metric_value_v1') }} v
    join definitions d on d.metric_id=v.metric_id and v.source is distinct from d.native_source
    where v.source <> '{{ raw_source }}' or v.metric_id not in (select metric_id from observation_ids)
    union all
    {% endif %}
    select o.metric_id, '{{ raw_source }}'::text as source,
           jsonb_build_object('id',p.id,'original_date',p.date,'source',p.source,'units',p.units,
                              'origin',p.origin,'health_record_id',p.health_record_id,
                              'source_snapshot_at',p.source_snapshot_at) as sample_key,
           to_jsonb(p.qty) as value,
           jsonb_build_object('observed_at',p.date,'available_at',p.received_at,'quality',p.quality) as source_metadata
    from observation_ids o cross join lateral (
        select * from {{ points }} p where p.metric_name=o.metric_id offset 0
    ) p
    {# A constant metric ID per branch lets PostgreSQL discard the other
       producers before evaluating a dated lookup. #}
    {% for group in models %}
    {% set producer = adapter.quote('producer_' ~ loop.index) %}
    {% for field in group.fields %}
    union all
    select '{{ field.metric | replace("'","''") }}'::text, '{{ group.model }}'::text,
           jsonb_build_object(
             {% for k in group['keys'] %}
             '{{ k }}', r.{{ adapter.quote(k) }}{{ ',' if not loop.last }}
             {% endfor %}
             {% if field.component %}, 'component', '{{ field.column }}'{% endif %}
           ), nullif(to_jsonb(r.{{ adapter.quote(field.column) }}), 'null'::jsonb),
           jsonb_build_object('column','{{ field.column }}'
               {% for attribute, column in group.metadata.items() %}, '{{ attribute }}', r.{{ adapter.quote(column) }}
               {% endfor %})
    from {{ producer }} r
    {% if field['filter'] %}
    where {% for k,v in field['filter'].items() %}
          r.{{ adapter.quote(k) }} = '{{ v | replace("'","''") }}'{{ ' and ' if not loop.last }}
          {% endfor %}
    {% if native and group.model == 'semantic_readiness_score_v1' and field.column != 'raw_score' %}and r.final_context{% endif %}
    {% endif %}
    {% endfor %}
    {% endfor %}
)
{{ profile_metric_rows('definitions', 'facts') }}
{% else %}
{{ profile_metric_rows(source('profile_metrics','metric_definition_v1'), source('profile_metrics','metric_value_v1')) }}
{% endif %}
