{{ config(materialized='ephemeral' if var('tenant_native_catalog', false) else 'view', enabled=var('energy_bank_shadow', false) or var('tenant_native_catalog', false), post_hook='alter view {{ this }} set (security_invoker=true)') }}

{% if var('tenant_native_catalog', false) %}
with decisions as (
    select current_user::text as profile,decision_key||':'||input_hash as capture_id,inputs
    from {{ source('source_data','hdbt_decision_inputs') }}
    where metric='energy_bands' and formula_version=1 and jsonb_typeof(inputs->'samples')='array'
), samples as (
    select d.profile,d.capture_id,s.sample,s.day,s.bank,1 as rn from decisions d
    cross join lateral jsonb_to_recordset(d.inputs->'samples') s(sample text,day text,bank int)
), {{ energy_calibrated_bands_v1() }}
select profile,capture_id,mode,latest_days,compatible_days,used_days,
       trunc(thresholds[1])::int as rest,trunc(thresholds[2])::int as recovery,trunc(thresholds[3])::int as push,
       case when mode='default_warmup' then 'default' else 'personal' end as source
from bands
{% else %}
with decisions as (select * from {{ source('energy_capture','decisions') }}),
{{ energy_verdict_bands_v1(source('energy_capture','band_history')) }}
select guidance.*,case when mode='default_warmup' then 'default' else 'personal' end as source
from ({{ energy_guidance_v1('bands') }}) as guidance
{% endif %}
