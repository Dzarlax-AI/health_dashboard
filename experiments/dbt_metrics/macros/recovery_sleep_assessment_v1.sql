{% macro recovery_sleep_assessment_v1(inputs) %}
with raw as (
    select i.*,coalesce(deep,0)+coalesce(rem,0)+coalesce(core,0) as staged,coalesce(unspecified,0) as coarse
    from {{ inputs }} i
), keyed as (
    select *,case when total is null then 'missing'
                  when total<=0 or total>14 then 'invalid'
                  when total<3.95 then case when staged>0 then 'short_staged' when coarse>0 then 'short_coarse' else 'short' end
                  when staged<=0 then case when coarse>0 then 'coarse' else 'no_stages' end
                  when abs(staged-total)/total>0.25 then 'mismatch' else 'good' end as class_key,
           case when total is null then 'sleep_data_missing'
                when total<3.95 or total>14 or total<=0 then 'sleep_total_out_of_range'
                when staged<=0 then 'coarse_only_source'
                when awake is null or awake=0 then
                     case when abs(staged-total)/total<=0.020000001 then 'ok_awake_structural_zero' else 'missing_awake_unknown' end
                when total+awake<=0 then 'missing_awake_unknown' else 'ok' end as eligibility_reason,
           case when total between 3.95 and 14 and staged>0 and
                    ((coalesce(awake,0)<>0 and total+awake>0) or
                     (coalesce(awake,0)=0 and abs(staged-total)/nullif(total,0)<=0.020000001))
                then case when coalesce(awake,0)=0 then 1.0 else total/(total+awake) end end as efficiency,
           case when total between 3.95 and 14 and staged>0 and
                    ((coalesce(awake,0)<>0 and total+awake>0) or
                     (coalesce(awake,0)=0 and abs(staged-total)/nullif(total,0)<=0.020000001))
                then total end as eligible_hours
    from raw
), classified as (
    select *,case class_key when 'missing' then 'missing_capture' when 'invalid' then 'invalid_total_capture'
           when 'short_staged' then 'partial_capture_short' when 'short_coarse' then 'coarse_only_capture'
           when 'short' then 'real_short_sleep_candidate' when 'coarse' then 'coarse_only_capture'
           when 'no_stages' then 'missing_capture' when 'mismatch' then 'stage_mismatch_capture'
           else 'good_capture' end as capture_class,
       case class_key when 'missing' then 0.0 when 'invalid' then 0.05
           when 'short_staged' then 0.35 when 'short_coarse' then 0.25 when 'short' then 0.20
           when 'coarse' then 0.55 when 'no_stages' then 0.15 when 'mismatch' then 0.50 else 0.95 end as capture_confidence,
       case class_key when 'missing' then 'no_sleep_total' when 'invalid' then 'invalid_sleep_total'
           when 'short_staged' then 'short_total_with_stages' when 'short_coarse' then 'short_total_coarse_only'
           when 'short' then 'invalid_sleep_total' when 'coarse' then 'no_staged_sleep'
           when 'no_stages' then 'no_staged_sleep' when 'mismatch' then 'stage_total_mismatch' else 'ok' end as capture_reason,
       class_key<>'good' as capture_low
    from keyed
)
select *, (efficiency is not null) as eligible from classified
{% endmacro %}
