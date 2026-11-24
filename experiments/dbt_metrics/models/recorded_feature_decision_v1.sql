{{ config(enabled=var('tenant_native_catalog',false), materialized='ephemeral') }}
select j.inputs->>'date' as date,j.inputs->>'sub_score' as sub_score,j.inputs->>'source_epoch' as source_epoch,
       (j.inputs->>'feature_version')::int as feature_version,j.captured_at as computed_at,
       j.decision_key||':'||j.input_hash as revision,e.*
from {{ source('source_data','hdbt_decision_inputs') }} j
cross join lateral {{ chip_feature_record('j.result') }}
where j.metric='chip_features' and j.formula_version=1
