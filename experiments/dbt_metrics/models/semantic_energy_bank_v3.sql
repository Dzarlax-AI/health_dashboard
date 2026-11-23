{{ config(enabled=var('energy_bank_shadow', false), materialized='view',
          post_hook='alter view {{ this }} set (security_invoker=true)') }}

{{ energy_bank_v3(source('energy_capture','runs'), source('energy_capture','days')) }}
