{% macro sleep_duration_comparison_fields() %}
cross join lateral (
    select max(minutes) filter (where kind = 'night') as duration_minutes,
           max(minutes) filter (where kind = 'reference') as rounded_reference
    from (
        select kind, floor(hours * 60)::integer
                     + ((hours * 60 - floor(hours * 60)) >= 0.5)::integer as minutes
        from (values ('night', duration_hours), ('reference', reference_hours)) as inputs(kind, hours)
        where hours > 0 and hours <= 24
    ) valid
) rounded
cross join lateral (
    select case when source is null then 'no_night'
                when duration_minutes is null or not (
                    capture_completeness = 'complete' and duration_assessment = 'plausible'
                    and (finalization_state = 'provisional'
                         or (finalization_state = 'final' and claim_eligibility = 'eligible'))
                ) then 'ineligible_night'
                when rounded_reference is null then 'insufficient_history'
                else 'available' end as status
    offset 0
) classified
cross join lateral (
    select case when status = 'available' then rounded_reference end as reference_minutes
) comparable
cross join lateral (
    select duration_minutes - reference_minutes as delta_minutes
) difference
{% endmacro %}
