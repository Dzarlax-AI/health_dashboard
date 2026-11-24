{% macro energy_calibrated_bands_v1() %}
percentiles as (
  select profile,capture_id,sample,count(*) as n,
         percentile_cont(array[0.20,0.50,0.80]) within group(order by bank) as thresholds
  from samples where rn=1 group by profile,capture_id,sample
), distribution as (
  select d.*,coalesce(l.n,0) as latest_days,coalesce(c.n,0) as compatible_days,
         l.thresholds as latest,c.thresholds as compatible,
         greatest(15,trunc(c.thresholds[1])::int) as provisional_rest,
         greatest(41,trunc(c.thresholds[2])::int) as provisional_recovery,
         greatest(55,trunc(c.thresholds[3])::int) as provisional_push
  from decisions d
  left join percentiles l on l.profile=d.profile and l.capture_id=d.capture_id and l.sample='latest'
  left join percentiles c on c.profile=d.profile and c.capture_id=d.capture_id and c.sample='compatible'
), calibration as (
  select *,case when latest_days>=30 then 'personal_latest_formula'
                when compatible_days>=30 then 'personal_mixed_formula_warmup'
                when compatible_days>=20 and provisional_rest<=provisional_recovery and provisional_recovery<provisional_push
                then 'provisional_compatible_formula_warmup' else 'default_warmup' end as mode
  from distribution
), bands as (
  select *,case mode when 'personal_latest_formula' then latest when 'personal_mixed_formula_warmup' then compatible
             when 'provisional_compatible_formula_warmup' then array[provisional_rest,provisional_recovery,provisional_push]
             else array[15,41,55] end as thresholds,
         case mode when 'personal_latest_formula' then latest_days when 'default_warmup' then 0 else compatible_days end as used_days
  from calibration
)
{% endmacro %}
