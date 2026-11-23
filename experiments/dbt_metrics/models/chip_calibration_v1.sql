{{ config(enabled=var('readiness_chips_shadow', false), materialized='view') }}

{{ chip_calibration_v1(source('chip_captures','chip_calibration_input'), source('chip_captures','chip_pairs')) }}
