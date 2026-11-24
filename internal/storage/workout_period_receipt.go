package storage

import dbtmetrics "health-receiver/experiments/dbt_metrics"

// Overlapping periods reuse immutable session inputs. A correction to one session
// appends its new revision and a period manifest. Empty periods have an empty manifest.
var workoutPeriodReceiptSQL = `with sessions as materialized (
    select id, duration_sec,distance_km,energy_kcal,avg_hr_bpm,max_hr_bpm,
        hr_z1_sec,hr_z2_sec,hr_z3_sec,hr_z4_sec,hr_z5_sec
    from workouts where start_time between $1 and $2 and ($3::text='' or name=$3)
    order by id
), packets as (
    select id,to_jsonb(s)-'id' as inputs from sessions s
), totals as (` + dbtmetrics.CapturedWorkoutPeriodSQL() + `), scope as (
    select jsonb_build_object('from',extract(epoch from $1::timestamptz),
        'to',extract(epoch from $2::timestamptz),'name_filter',$3::text) as key
), decision_packets as (
    select 'workout_session'::text metric,id::text decision_key,1 formula_version,inputs,'{}'::jsonb result from packets
    union all
    select 'workout_period',scope.key::text,1,
        scope.key || jsonb_build_object('sessions',coalesce((select jsonb_agg(
            jsonb_build_array(id,encode(sha256(convert_to(jsonb_build_array(1,inputs)::text,'UTF8')),'hex')) order by id)
            from packets),'[]'::jsonb)),to_jsonb(totals)
    from scope cross join totals
)` + decisionJournalInsertSQL + ` select * from totals`
