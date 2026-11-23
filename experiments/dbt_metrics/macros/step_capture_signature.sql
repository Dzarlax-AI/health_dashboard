{% macro step_capture_signature(points) %}
select substring(date,1,13)||':00' as hour,
       encode(sha256(convert_to(jsonb_agg(jsonb_build_array(date,source,grain,qty,quality,stored_key)
                     order by date collate "C",source collate "C",grain collate "C")::text,'UTF8')),'hex') as signature
from {{ points }} group by substring(date,1,13)
{% endmacro %}
