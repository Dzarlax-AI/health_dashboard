{{ config(enabled=var('readiness_chips_shadow', false), materialized='view') }}

{{ chip_targets_v1(source('chip_captures','chip_target_input')) }}
