{{ config(enabled=var('archived_profile_fields', false), materialized='view',
    post_hook='alter view {{ this }} set (security_invoker=true)') }}

-- Match GetEnergyHistory's zero-valued integers for partially populated legacy rows.
select date, coalesce(energy_capacity, 0) as capacity,
       coalesce(energy_eod_current, 0) as current_eod,
       coalesce(energy_drain, 0) as drain, energy_verdict as verdict
from {{ source('archive_records', 'daily_scores') }}
where energy_verdict is not null
