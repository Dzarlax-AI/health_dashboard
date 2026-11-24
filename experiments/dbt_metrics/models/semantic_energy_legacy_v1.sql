{{ config(materialized='ephemeral' if var('tenant_native_catalog', false) else 'view', enabled=var('energy_bank_shadow', false) or var('tenant_native_catalog', false), post_hook='alter view {{ this }} set (security_invoker=true)') }}

{% if var('tenant_native_catalog', false) %}
with selected as (
    select current_user::text as profile,j.decision_key||':'||j.input_hash as capture_id,i.*
    from {{ source('source_data','hdbt_decision_inputs') }} j
    cross join lateral jsonb_to_record(j.inputs) i(readiness int,headline text,sleep float8[],hrv float8[],rhr float8[],
        steps float8,usual_steps float8,kcal float8,usual_kcal float8)
    where j.metric='energy_legacy' and j.formula_version=1
      and j.inputs ?& array['readiness','headline','sleep','hrv','rhr','steps','usual_steps','kcal','usual_kcal']
)
{% endif %}
select profile,capture_id,state,capacity,current,drained,strain,stress,hrv_z,verdict,reason
from ({{ energy_legacy_v1('selected' if var('tenant_native_catalog', false) else source('energy_capture','legacy')) }}) as legacy
