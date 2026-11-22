WITH classified AS (
    SELECT * FROM {{ ref('research_packet_onset_base_v1') }}
)
{{ pair_research_onset() }}
