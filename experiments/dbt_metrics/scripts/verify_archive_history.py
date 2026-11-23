"""Print SQL for a disposable local archive with a source_data schema.

Checks the actual SQL of four archived-history models, and the HRV baseline
for each retained calendar day under an explicit retrospective timezone.
Outputs only counts (including non-NULL candidate values), never health data.
"""
from pathlib import Path

from jinja2 import Environment, StrictUndefined

ROOT = Path(__file__).resolve().parents[1]
HISTORY = {
    'archived_wake_time_v1': ('metric_date, state, inputs_hash', 'derived_metrics',
        "where metric_name='wake_time' and value_type='timestamp'", {'wake_time': ('value_timestamp', 'value_timestamp')}),
    'archived_sleep_goal_v1': ('effective_date, version', 'sleep_goal', '',
        {'sleep_goal.hours': ('goal_hours', 'goal_hours')}),
    'archived_energy_eod_v1': ('date, ts_bucket', 'energy_snapshots', "where 'backfilled'=any(flags)",
        {'energy_eod_backfill': ('bank', 'bank')}),
    'archived_legacy_energy_eod_v1': ('date', 'daily_scores', 'where energy_verdict is not null',
        {'legacy_energy_eod.' + k: (k, ref) for k, ref in [
            ('current_eod', 'coalesce(energy_eod_current,0)'), ('capacity', 'coalesce(energy_capacity,0)'),
            ('verdict', 'energy_verdict'), ('drain', 'coalesce(energy_drain,0)')]}),
}


def comparison(metric, keys, ref, candidate):
    return f"""
with reference_rows as ({ref}), candidate_rows as ({candidate}),
difference as (
    (select * from reference_rows except all select * from candidate_rows)
    union all
    (select * from candidate_rows except all select * from reference_rows)
)
select json_build_array('{metric}', (select count(*) from reference_rows),
    (select count(*) from candidate_rows), (select count(v) from candidate_rows),
    (select count(*) from difference));"""


def render():
    env = Environment(undefined=StrictUndefined)
    env.globals.update(config=lambda **kwargs: '', var=lambda key, default=None: default,
                       source=lambda schema, name: 'source_data.' + name, ref=lambda name: name)
    for name in ('personal_baseline_result', 'personal_baseline_stats'):
        module = env.from_string((ROOT / 'macros' / (name + '.sql')).read_text()).module
        env.globals[name] = getattr(module, name)

    def model(name):
        query = env.from_string((ROOT / 'models' / (name + '.sql')).read_text()).render()
        return 'create temporary view ' + name + ' as ' + query.strip() + ';'

    for name, (keys, table, predicate, fields) in HISTORY.items():
        yield model(name)
        for metric, (column, expr) in fields.items():
            yield comparison(metric, keys, f'select {keys},{expr} as v from source_data.{table} {predicate}',
                             f'select {keys},{column} as v from {name}')
    yield """
create temporary table eval_days as
select date::date as anchor from source_data.daily_scores;
create table source_data.captures as
select 'local'::text as profile, row_number() over(order by anchor) as capture, 'hrv'::text as channel,
       anchor, 'Europe/Belgrade'::text as zone, 30 as window_days from eval_days;
create table source_data.selected_samples as
select 'local'::text as profile, 'hrv'::text as channel, left(date,10)::date as writer_day,
       date::timestamptz as observed_at, qty as value
from source_data.metric_points
where metric_name='heart_rate_variability' and quality='ok'
  and (substring(date,12,2)>='23' or substring(date,12,2)<'09');
create index on source_data.selected_samples(writer_day);
create index on source_data.metric_points(date);
analyze source_data.selected_samples;
analyze source_data.metric_points;
"""
    yield model('personal_baseline_v1')
    yield model('hrv_fixed_window_baseline_v1')
    # The production reader compares original timestamp strings including the offset.
    yield "set time zone 'Europe/Belgrade';"
    yield comparison('hrv_fixed_window_baseline_v1', 'anchor', """
select d.anchor, case when r.n>=3 then r.median end as v
from eval_days d cross join lateral (
 select count(*) as n,percentile_cont(0.5) within group(order by qty) as median
 from source_data.metric_points m
 where m.metric_name='heart_rate_variability' and m.quality='ok'
   and (substring(m.date,12,2)>='23' or substring(m.date,12,2)<'09')
   and m.qty>'-Infinity'::float8 and m.qty<'Infinity'::float8
   and m.date>=to_char((d.anchor-30)::timestamptz,'YYYY-MM-DD HH24:MI:SS TZHTZM')
   and m.date<to_char(d.anchor::timestamptz,'YYYY-MM-DD HH24:MI:SS TZHTZM')
) r""", 'select anchor,baseline_median as v from hrv_fixed_window_baseline_v1')


if __name__ == '__main__':
    print('\n'.join(render()))
