{% macro legacy_sleep_selection_v1(relation, priority, min_hours, divergence, traditional, traditional_count, coarse, coarse_count) %}
sleep_totals AS (
    SELECT date, source, sum_val FROM {{ relation }} WHERE metric_name = 'sleep_total'
),
sleep_stats AS (
    SELECT date, COUNT(*) AS n_sources, MIN(sum_val) AS min_total, MAX(sum_val) AS max_total
    FROM sleep_totals GROUP BY date
),
sleep_ranked AS (
    SELECT st.date, st.source,
           ROW_NUMBER() OVER (PARTITION BY st.date ORDER BY
             CASE WHEN ss.n_sources > 1 AND ss.min_total > {{ min_hours }}
                        AND ss.max_total > ss.min_total * {{ divergence }} THEN 0
                  ELSE {{ priority }} END,
             CASE WHEN ss.n_sources > 1 AND ss.min_total > {{ min_hours }}
                        AND ss.max_total > ss.min_total * {{ divergence }} THEN st.sum_val END ASC,
             CASE WHEN NOT (ss.n_sources > 1 AND ss.min_total > {{ min_hours }}
                        AND ss.max_total > ss.min_total * {{ divergence }}) THEN st.sum_val END DESC,
             st.source ASC) AS rank
    FROM sleep_totals st JOIN sleep_stats ss USING (date)
),
picked AS (SELECT date, source FROM sleep_ranked WHERE rank = 1),
sleep_gate AS (
    SELECT p.date,
      (ss.n_sources <= 1 OR
       COUNT(DISTINCT ps.metric_name) FILTER (WHERE ps.metric_name IN
         ({{ traditional }})) = {{ traditional_count }} OR
       COUNT(DISTINCT ps.metric_name) FILTER (WHERE ps.metric_name IN
         ({{ coarse }})) = {{ coarse_count }}) AS complete
    FROM picked p JOIN sleep_stats ss USING(date)
    LEFT JOIN {{ relation }} ps ON ps.date=p.date AND ps.source=p.source
    GROUP BY p.date, ss.n_sources
)
{% endmacro %}
