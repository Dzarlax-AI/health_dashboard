{{ config(enabled=var('profile_metric_reads', false), materialized='view',
          grants={'select': var('profile_metric_read_roles', [])},
          post_hook="alter view {{ this }} set (security_invoker=true)") }}

{% set models = var('profile_graph_models', []) %}
{% if models %}
with non_values as (
    select key as metric_id, value->>0 as disposition, value->>1 as reason
    from jsonb_each('{{ tojson(var("profile_graph_non_values")) | replace("'","''") }}'::jsonb)
), definitions as (
    select c.metric_id, c.definition || jsonb_build_object('metric_id',c.metric_id) as definition,
           coalesce(n.disposition,'active') as disposition, n.reason,
           coalesce(d.read_sources,'[]'::jsonb) ||
               case when c.dbt_binding is null or d.read_sources ? (c.dbt_binding->>'model') then '[]'::jsonb
                    else jsonb_build_array(c.dbt_binding->>'model') end as read_sources,
           c.dbt_binding
    from {{ ref('semantic_metric_catalog_v1') }} c
    left join non_values n using (metric_id)
    left join {{ source('profile_metrics','metric_definition_v1') }} d using (metric_id)
), facts as (
    select v.metric_id, v.source, v.sample_key, v.value, v.source_metadata
    from {{ source('profile_metrics','metric_value_v1') }} v
    join definitions d on d.metric_id=v.metric_id and v.source is distinct from d.dbt_binding->>'model'
    {# A constant metric ID per branch lets PostgreSQL discard the other
       producers before evaluating a dated lookup. #}
    {% for group in models %}
    {% for field in group.fields %}
    union all
    select '{{ field.metric | replace("'","''") }}'::text, '{{ group.model }}'::text,
           jsonb_build_object(
             {% for k in group['keys'] %}
             '{{ k }}', r.{{ adapter.quote(k) }}{{ ',' if not loop.last }}
             {% endfor %}
             {% if field.component %}, 'component', '{{ field.column }}'{% endif %}
           ), nullif(to_jsonb(r.{{ adapter.quote(field.column) }}), 'null'::jsonb),
           jsonb_build_object('column','{{ field.column }}')
    from {{ ref(group.model) }} r
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
