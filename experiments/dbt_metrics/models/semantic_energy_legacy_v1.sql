{{ config(materialized='view', enabled=var('energy_bank_shadow', false), post_hook='alter view {{ this }} set (security_invoker=true)') }}

select profile,capture_id,state,capacity,current,drained,strain,stress,hrv_z,verdict,reason
from ({{ energy_legacy_v1(source('energy_capture','legacy')) }}) as legacy
