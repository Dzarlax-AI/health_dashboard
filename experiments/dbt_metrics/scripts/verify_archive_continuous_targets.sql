with expected as (
    select concat_ws('/',sub_score,target_kind,date) as case_id,sub_score,target_kind,target_value,eligible,eligibility_reason
    from source_data.target_snapshots where sub_score='recovery_stability' or (sub_score='passive_efficiency' and target_kind='rolling_3d')
), rows as (
    select coalesce(e.sub_score,m.sub_score)||'.target_'||coalesce(e.target_kind,m.target_kind) as metric,
           e.case_id is not null as old_present,m.case_id is not null as new_present,
           (e.case_id,e.eligible,e.eligibility_reason,e.target_value) is distinct from
           (m.case_id,m.eligible,m.reason,m.target_value::real) as differs
    from expected e full join archive_formula.chip_targets_v1 m using(case_id)
)
select json_build_array(metric,count(*) filter(where old_present),count(*) filter(where new_present),
    count(*) filter(where differs)) from rows group by metric order by metric;
