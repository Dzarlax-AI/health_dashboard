{{ config(enabled=var('readiness_chips_shadow', false), materialized='view') }}

-- Replay uses the cutoff supplied by the historical reader. A decision-time
-- capture includes its own cutoff. Never recover a missing morning from a replay.
select case_id, capture_mode,
       case when capture_mode='unrecoverable' then null
            when epoch_changed or (predicted is null and baseline_reason is not null) then 'unknown'
            when predicted is null then 'pending'
            when sub_score not in ('acute_risk','chronic_load') then 'value'
            when cutoff is null or calibration_status <> 'active' then 'calibrating'
            when predicted >= cutoff then 'elevated' else 'ok' end as chip_state,
       case when capture_mode <> 'unrecoverable' and not epoch_changed
            and sub_score not in ('acute_risk','chronic_load') then predicted end as numeric_value
from {{ source('chip_captures','chip_display_input') }}
