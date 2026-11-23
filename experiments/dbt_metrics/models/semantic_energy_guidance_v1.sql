{{ config(materialized='view', enabled=var('energy_bank_shadow', false), post_hook='alter view {{ this }} set (security_invoker=true)') }}

with decisions as (select * from {{ source('energy_capture','decisions') }}),
{{ energy_verdict_bands_v1(source('energy_capture','band_history')) }}
select guidance.*,case when mode='default_warmup' then 'default' else 'personal' end as source
from ({{ energy_guidance_v1('bands') }}) as guidance
