{{ config(enabled=var('sleep_daily_shadow', false) and not var('sleep_daily_runtime_sql', false)) }}

WITH candidates(profile,wake_date,id,revision,priority,snapshot) AS (
    VALUES
      ('fixture',date '2026-01-26','watch',2,0,'{"zero_stages":["awake"],"stages":[
        {"kind":"core","start_at":"2026-01-25T22:00Z","end_at":"2026-01-25T23:00Z"},
        {"kind":"core","start_at":"2026-01-25T22:30Z","end_at":"2026-01-25T23:00Z"},
        {"kind":"asleep_unspecified","start_at":"2026-01-25T22:30Z","end_at":"2026-01-26T00:00Z"},
        {"kind":"deep","start_at":"2026-01-26T00:15Z","end_at":"2026-01-26T01:00Z"}
      ]}'::jsonb),
      ('fixture',date '2026-01-26','secondary',1,1,'{"stages":[
        {"kind":"rem","start_at":"2026-01-25T22:00Z","end_at":"2026-01-26T02:00Z"}
      ]}'::jsonb),
      ('fixture',date '2026-01-27','coarse',1,1,'{"stages":[
        {"kind":"asleep_unspecified","start_at":"2026-01-26T22:00Z","end_at":"2026-01-27T01:00Z"}
      ]}'::jsonb),
      ('fixture',date '2026-01-28','conflict',1,0,'{"stages":[
        {"kind":"deep","start_at":"2026-01-27T22:00Z","end_at":"2026-01-28T00:00Z"},
        {"kind":"rem","start_at":"2026-01-27T23:00Z","end_at":"2026-01-28T01:00Z"}
      ]}'::jsonb)
), modeled AS (
    {{ shadow_sleep_daily_v1('candidates', 'true') }}
), actual AS (
    SELECT wake_date, candidate, revision, sleep_seconds, deep_seconds, rem_seconds,
           core_seconds, unspecified_seconds, awake_seconds, stage_conflict
    FROM modeled
), expected(wake_date,candidate,revision,sleep_seconds,deep_seconds,rem_seconds,
            core_seconds,unspecified_seconds,awake_seconds,stage_conflict) AS (
    VALUES (date '2026-01-26','watch',2,9900,2700,null::double precision,3600,3600,0,false),
           (date '2026-01-27','coarse',1,10800,null,null,null,10800,null,false),
           (date '2026-01-28','conflict',1,10800,null,null,null,null,null,true)
)
(SELECT * FROM actual EXCEPT ALL SELECT * FROM expected)
UNION ALL
(SELECT * FROM expected EXCEPT ALL SELECT * FROM actual)
