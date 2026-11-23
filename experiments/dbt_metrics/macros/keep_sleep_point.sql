{% macro keep_sleep_point(relation, outer_row) %}
NOT (
		SUBSTRING(date, 12, 8) != '00:00:00'
		AND EXISTS (
			SELECT 1 FROM {{ relation }} p2
			WHERE p2.metric_name = {{ outer_row }}.metric_name
			  AND SUBSTRING(p2.date, 1, 10) = SUBSTRING({{ outer_row }}.date, 1, 10)
			  AND p2.source = {{ outer_row }}.source
			  AND SUBSTRING(p2.date, 12, 8) = '00:00:00'
			  AND p2.qty > 0
			  AND p2.quality = 'ok'
		)
	)
{% endmacro %}
