{% macro weekly_sleep_round(value, scale) %}
    /* Keep the binary fractional part for Go math.Round's half-away rule.
       Casting to numeric first can move a value just below a half onto it. */
    (
        trunc(({{ value }}) * {{ scale }})
        + case when abs(({{ value }}) * {{ scale }} - trunc(({{ value }}) * {{ scale }})) >= 0.5
               then sign({{ value }}) else 0 end
    ) / {{ scale }}
{% endmacro %}
