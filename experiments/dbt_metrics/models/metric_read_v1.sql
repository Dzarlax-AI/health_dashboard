{{ config(enabled=var('profile_metric_reads', false), materialized='view',
          grants={'select': var('profile_metric_read_roles', [])},
          post_hook="alter view {{ this }} set (security_invoker=true)") }}

{% set models = var('profile_graph_models', []) %}
{% set observation_ids = var('profile_graph_observations', []) %}
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
           coalesce(d.read_sources,'[]'::jsonb) ||
               case when o.metric_id is not null and not coalesce(d.read_sources ? '{{ raw_source }}', false)
                    then jsonb_build_array('{{ raw_source }}'::text) else '[]'::jsonb end ||
               case when nsrc.name is null or d.read_sources ? nsrc.name then '[]'::jsonb
                    else jsonb_build_array(c.dbt_binding->>'model') end as read_sources,
           c.dbt_binding, nsrc.name as native_source
    from {{ ref('semantic_metric_catalog_v1') }} c
    left join non_values n using (metric_id)
    left join observation_ids o using (metric_id)
    left join native_sources nsrc on nsrc.name=c.dbt_binding->>'model'
    left join {{ source('profile_metrics','metric_definition_v1') }} d using (metric_id)
), {% for group in models %}
{{ adapter.quote('producer_' ~ loop.index) }} as {% if group.model == 'daily_metrics' or group.model.startswith('archived_') %}not {% endif %}materialized (
    select * from {{ ref(group.model) }}
),
{% endfor %}
facts as (
    select v.metric_id, v.source, v.sample_key, v.value, v.source_metadata
    from {{ source('profile_metrics','metric_value_v1') }} v
    join definitions d on d.metric_id=v.metric_id and v.source is distinct from d.native_source
    where v.source <> '{{ raw_source }}' or v.metric_id not in (select metric_id from observation_ids)
    union all
    select o.metric_id, '{{ raw_source }}'::text,
           jsonb_build_object('id',p.id,'original_date',p.date,'source',p.source,'units',p.units,
                              'origin',p.origin,'health_record_id',p.health_record_id,
                              'source_snapshot_at',p.source_snapshot_at),
           to_jsonb(p.qty),
           jsonb_build_object('observed_at',p.date,'available_at',p.received_at,'quality',p.quality)
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
    {% endif %}
    {% endfor %}
    {% endfor %}
)
{{ profile_metric_rows('definitions', 'facts') }}
{% else %}
{{ profile_metric_rows(source('profile_metrics','metric_definition_v1'), source('profile_metrics','metric_value_v1')) }}
{% endif %}
