{% macro readiness_effective_eligibility_v1(inputs) %}
select i.*, h.*, case when reader='morning' and not hrv_present then 'missing_same_day_evidence'
		when coalesce(cardinality(hrv),0)=0 then 'missing_same_day_value'
		when cardinality(hrv)<9 then 'insufficient_observations' end as skip_reason
	from {{ inputs }} i cross join lateral (
		select max(v) filter(where pos=1) as current_ms, avg(v) filter(where pos<=7) as recent7_mean_ms,
		       avg(v) filter(where pos>7) as baseline_mean_ms, stddev_pop(v) filter(where pos>7) as baseline_sd_ms
		from unnest(i.hrv) with ordinality as a(v,pos)
	) h
{% endmacro %}
