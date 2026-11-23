{{ config(enabled=var('briefing_components_shadow', false), materialized='view', post_hook="alter view {{ this }} set (security_invoker = true)") }}

with captures as (
    select profile, capture, inputs, inputs->>'workout_end' as end_text
    from {{ source('briefing_pilot','captured_inputs') }}
), prepared as (
    select profile, capture, coalesce(inputs->'zone_input', jsonb_build_object(
        'end_second', extract(epoch from regexp_replace(end_text, '\.[0-9]+', '')::timestamptz),
        'end_nano', coalesce(rpad(substring(end_text from '\.([0-9]+)'), 9, '0')::integer,0),
        'borders', string_to_array(inputs->>'zones', ','),
        'samples', (select coalesce(jsonb_agg(jsonb_build_object(
                  'second', sample_second, 'nano', sample_nano, 'avg', bpm)
                order by sample_second, sample_nano, ordinal), '[]'::jsonb)
            from (select extract(epoch from regexp_replace(sample->>'Time', '\.[0-9]+', '')::timestamptz) as sample_second,
                         coalesce(rpad(substring(sample->>'Time' from '\.([0-9]+)'), 9, '0')::integer,0) as sample_nano,
                         sample->'Avg' as bpm, ordinal
                  from jsonb_array_elements(inputs->'hr_samples') with ordinality as raw(sample, ordinal)
                  where inputs->>'zones' <> '') samples)
    )) as zone_input
    from captures
)
select profile, capture, z.zone_seconds
from prepared cross join lateral (
    {{ workout_zone_seconds('zone_input') }}
) z
