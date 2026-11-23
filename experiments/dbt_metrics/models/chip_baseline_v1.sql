{{ config(enabled=var('readiness_chips_shadow', false), materialized='view') }}

{{ chip_baseline_v1(source('chip_captures','chip_baseline_input')) }}
