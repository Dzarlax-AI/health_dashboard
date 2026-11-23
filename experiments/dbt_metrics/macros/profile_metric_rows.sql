{% macro profile_metric_rows(definitions, facts) %}
select d.metric_id, d.definition, d.disposition, d.reason, v.source, v.sample_key,
       d.read_sources, d.dbt_binding, v.value, v.source_metadata,
       v.source_metadata->>'observed_at' as original_observation_time,
       v.source_metadata->>'available_at' as available_at,
       v.source_metadata->>'version' as source_version,
       case when jsonb_typeof(v.value) = 'number' then (v.value #>> '{}')::numeric end as numeric_value,
       case when jsonb_typeof(v.value) = 'boolean' then (v.value #>> '{}')::boolean end as boolean_value,
       case when jsonb_typeof(v.value) = 'string' then v.value #>> '{}' end as text_value,
       case when d.disposition <> 'active' then d.disposition
            when v.metric_id is null then 'no_row'
            when v.value is null then 'missing'
            else 'available' end as value_status
from {{ definitions }} d left join {{ facts }} v using (metric_id)
{% endmacro %}
