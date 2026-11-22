{{ config(enabled=var('preferred_sleep_source_shadow',false),materialized='view',post_hook="alter view {{ this }} set (security_invoker = true)") }}

-- The packet selector owns qualification, ambiguity and episode grouping.
-- Read its stored decision for this tenant. Earlier rows still identify the
-- winning packet but do not contain the preference used to produce it.
select wake_date,generation,candidate,revision,sleep_seconds,
       episode_selection->>'policy' as selection_version,
       candidate::jsonb->>0 as selected_source,
       episode_selection->>'preferred_sleep_source' as preferred_sleep_source,
       episode_selection->>'source_selection_reason' as source_selection_reason
from {{ source('preferred_sleep_source','sleep_rollout_daily') }}
