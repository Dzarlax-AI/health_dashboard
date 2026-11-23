{% macro prepare_metric_capture() %}
{% if is_incremental() %}
    {% if var('aggregate_capture_mode','snapshot') == 'snapshot' %}
        delete from {{ this }} where {{ affected_dates_filter('substring(date,1,10)') }}
    {% elif var('aggregate_capture_mode','snapshot') == 'partial' and var('verified_step_withdrawals',false) %}
        delete from {{ this }} p using {{ source('source_data','verified_step_withdrawals') }} w
        where p.metric_name='step_count' and (p.date,p.source)=(w.point_date,w.source)
          and {{ affected_dates_filter('substring(p.date,1,10)') }}
    {% endif %}
{% endif %}
{% endmacro %}
