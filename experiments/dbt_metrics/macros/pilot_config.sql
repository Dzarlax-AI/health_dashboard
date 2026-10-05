{% macro affected_dates_filter(column_name) -%}
  {% if var('affected_dates', []) | length > 0 %}
    {{ column_name }} = ANY(ARRAY[
      {% for date_value in var('affected_dates') %}
        '{{ date_value }}'{% if not loop.last %}, {% endif %}
      {% endfor %}
    ]::text[])
  {% else %}
    TRUE
  {% endif %}
{%- endmacro %}

{% macro validate_affected_dates() %}
  {% set values = var('affected_dates', []) %}
  {% if values is string or values is mapping or values is not sequence %}
    {{ exceptions.raise_compiler_error('affected_dates must be a list of YYYY-MM-DD strings') }}
  {% endif %}
  {% if flags.FULL_REFRESH and values | length > 0 %}
    {{ exceptions.raise_compiler_error('full refresh requires the complete calendar; omit affected_dates') }}
  {% endif %}
  {% if is_incremental() and (values | length == 0) %}
    {{ exceptions.raise_compiler_error('incremental dbt pilot runs require explicit --vars affected_dates') }}
  {% endif %}
  {% for value in values %}
    {% if value is not string or (value | length != 10) or (value[4:5] != '-') or (value[7:8] != '-') %}
      {{ exceptions.raise_compiler_error('affected_dates must contain YYYY-MM-DD values') }}
    {% endif %}
    {% for char in value %}
      {% if char not in '0123456789-' %}
        {{ exceptions.raise_compiler_error('affected_dates must contain YYYY-MM-DD values') }}
      {% endif %}
    {% endfor %}
    {% set parsed_date = modules.datetime.datetime.strptime(value, '%Y-%m-%d') %}
    {% if parsed_date.strftime('%Y-%m-%d') != value %}
      {{ exceptions.raise_compiler_error('affected_dates must contain valid calendar dates') }}
    {% endif %}
  {% endfor %}
  {% if execute and values | length > 0 %}
    {% set sql %}
      SELECT requested.date
      FROM unnest(ARRAY[
        {% for value in values %}'{{ value }}'{% if not loop.last %}, {% endif %}{% endfor %}
      ]::text[]) requested(date)
      LEFT JOIN source_data.calendar c ON c.date = requested.date
      WHERE c.date IS NULL
    {% endset %}
    {% set missing = run_query(sql) %}
    {% if missing.rows | length > 0 %}
      {{ exceptions.raise_compiler_error('affected_dates must exist in source_data.calendar') }}
    {% endif %}
  {% endif %}
{% endmacro %}

{% macro delete_hourly_affected_days() %}
  {% if is_incremental() and var('affected_dates', []) | length > 0 %}
    DELETE FROM {{ this }} WHERE SUBSTRING(hour, 1, 10) = ANY(ARRAY[
      {% for date_value in var('affected_dates') %}
        '{{ date_value }}'{% if not loop.last %}, {% endif %}
      {% endfor %}
    ]::text[])
  {% endif %}
{% endmacro %}
