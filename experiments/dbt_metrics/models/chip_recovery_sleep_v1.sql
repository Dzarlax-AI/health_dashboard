{{ config(enabled=var('readiness_chips_shadow', false), materialized='view') }}

{{ chip_recovery_sleep_v1(source('chip_captures','chip_recovery_sleep_input')) }}
