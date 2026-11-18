{{ config(enabled=var('semantic_sleep_cache', false), materialized='incremental',
    unique_key='wake_date', incremental_strategy='delete+insert',
    indexes=[{'columns': ['wake_date'], 'unique': true}],
    pre_hook="select pg_advisory_xact_lock(174913, hashtext('{{ target.schema }}'))",
    post_hook="delete from {{ this }} where input_duration_hours is null") }}

{% set nights = source('source_data', 'completed_night_sleep') %}
{% set compared = ref('semantic_sleep_duration_comparison_v1') %}

-- Compare canonical inputs so backdated edits and removals survive a missed run.
-- All affected values come from one source snapshot in the staging statement.
with
{% if is_incremental() %}
changed as materialized (
    select wake_date from {{ nights }} n
    full join {{ this }} c using (wake_date)
    where n.wake_date is null or c.wake_date is null
       or (n.duration_hours, n.source, n.source_epoch, n.algorithm_version, n.input_hash,
           n.capture_completeness, n.duration_assessment, n.finalization_state, n.claim_eligibility, n.observed_at)
          is distinct from
          (c.input_duration_hours, c.source, c.source_epoch, c.algorithm_version, c.input_hash,
           c.capture_completeness, c.duration_assessment, c.finalization_state, c.claim_eligibility, c.observed_at)
), affected as (
    select distinct (changed.wake_date::date + days.i)::text as wake_date
    from changed cross join generate_series(0,30) as days(i)
),
{% endif %}
rebuild as (
    select n.wake_date, n.duration_hours as input_duration_hours from {{ nights }} n
    {% if is_incremental() %}
    join affected using (wake_date)
    union all
    -- Stage the removed key so delete+insert clears the old row. The post-hook
    -- removes its tombstone in that same transaction before readers see it.
    select c.wake_date, null::double precision from {{ this }} c join changed using (wake_date)
    where not exists (select from {{ nights }} n where n.wake_date=c.wake_date)
    {% endif %}
)
select wake_date, b.input_duration_hours, f.status, f.duration_minutes, f.reference_minutes,
       f.delta_minutes, f.eligible_nights, f.window_start, f.window_end_exclusive, f.observed_at,
       f.source, f.source_epoch, f.algorithm_version, f.input_hash, f.capture_completeness,
       f.duration_assessment, f.finalization_state, f.claim_eligibility, f.history_version
from rebuild b left join {{ compared }} f using (wake_date)
