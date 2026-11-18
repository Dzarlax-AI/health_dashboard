{{ config(enabled=var('semantic_night_card_cache', false), materialized='incremental',
    unique_key='wake_date', incremental_strategy='delete+insert',
    indexes=[{'columns': ['wake_date'], 'unique': true}],
    pre_hook="select pg_advisory_xact_lock(174914, hashtext('{{ target.schema }}'))",
    post_hook="delete from {{ this }} where card is null") }}

-- Re-evaluate the local calendar in one snapshot. Equality includes every
-- visible field and its basis. Tombstones clear removed calendar dates in the
-- same transaction as replacements. This buys read speed at full refresh cost.
{% if is_incremental() %}
select wake_date,live.card
from {{ ref('semantic_sleep_night_card_v1') }} live
full join {{ this }} old using(wake_date)
where live.card is distinct from old.card
{% else %}
select wake_date,card from {{ ref('semantic_sleep_night_card_v1') }}
{% endif %}
