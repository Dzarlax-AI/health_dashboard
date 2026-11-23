create schema baseline_fixture;
create table baseline_fixture.captures(profile text, capture int, channel text, anchor date,
    zone text, window_days int, primary key(profile,capture));
create table baseline_fixture.selected_samples(profile text, channel text, writer_day date,
    observed_at timestamptz, value float8, quality text, observed_hour int);
insert into baseline_fixture.selected_samples
select profile, channel, date '2001-03-01' + day_no,
       (date '2001-03-01' + day_no + make_time(hr,0,0)) at time zone 'Europe/Belgrade',
       case when day_no = 21 then null when day_no = 22 then 'NaN'::float8
            when channel = 'temp' then 36.4 + adjustment
            else 50 + adjustment + mod(day_no,9) * 2 end,
       'ok', hr
from (values ('fixture_one',0),('fixture_two',10)) p(profile,adjustment)
cross join (values ('hr_awake',8),('hr_overnight',0),('hrv',2),('resp',8),('temp',8)) ch(channel,hr)
cross join generate_series(0,22) days(day_no);
-- Adapter rejections. The Go reader receives these raw points too.
insert into baseline_fixture.selected_samples values
 ('fixture_one','hrv','2001-03-03','2001-03-03 12:00+01',999,'ok',12),
 ('fixture_one','resp','2001-03-04','2001-03-04 09:00+01',888,'suspect',9);
create table baseline_fixture.raw_inputs as select * from baseline_fixture.selected_samples;
delete from baseline_fixture.selected_samples where quality <> 'ok' or (channel='hrv' and observed_hour between 9 and 22);
insert into baseline_fixture.captures
select profile, row_number() over(partition by profile order by channel, day_no, win), channel,
       date '2001-03-01' + day_no, 'Europe/Belgrade', win
from (values ('fixture_one'),('fixture_two')) p(profile)
cross join (values ('hr_awake'),('hr_overnight'),('hrv'),('resp'),('temp')) ch(channel)
cross join (values (0),(2),(3),(6),(7),(13),(14),(21),(23),(33),(35),(36),(48)) d(day_no)
cross join (values (0),(5)) w(win);
-- The fixtures expose the three projections used by the production storage
-- reader in a separate PostgreSQL schema for each synthetic profile.
do $setup$
declare tenant text;
begin
  foreach tenant in array array['fixture_one','fixture_two'] loop
    execute format('create schema %I', tenant);
    execute format($q$create view %I.hourly_metrics as
      select 'heart_rate'::text as metric_name, value as avg_val,
        to_char(observed_at at time zone 'Europe/Belgrade','YYYY-MM-DD HH24:MI') as hour
      from baseline_fixture.raw_inputs where profile=%L and channel='hr_awake' and value is not null$q$,tenant,tenant);
    execute format($q$create view %I.daily_scores as select writer_day::text as date, value as baseline_hr_overnight
      from baseline_fixture.raw_inputs where profile=%L and channel='hr_overnight'$q$,tenant,tenant);
    execute format($q$create view %I.metric_points as select value as qty, quality, case channel
      when 'hrv' then 'heart_rate_variability' when 'resp' then 'respiratory_rate' else 'wrist_temperature' end as metric_name,
      to_char(observed_at,'YYYY-MM-DD HH24:MI:SS TZH') || '00' as date
      from baseline_fixture.raw_inputs where profile=%L and channel in ('hrv','resp','temp') and value is not null$q$,tenant,tenant);
  end loop;
end $setup$;

-- A legacy text clock can carry more precision than PostgreSQL timestamptz.
create schema fixture_clock;
create table fixture_clock.metric_points(metric_name text, quality text, date text, qty real);
insert into fixture_clock.metric_points values
 ('respiratory_rate','ok','2001-03-01 23:59:59.999999999 +0000',42),
 ('respiratory_rate','ok','2001-03-01 23:59:59.999999998 +0000',42),
 ('respiratory_rate','ok','2001-03-02 00:00:00 +0100',42),
 ('respiratory_rate','ok','2001-03-01 23:00:00 +0000',42),
 ('respiratory_rate','ok','2001-03-01 22:00:00 +0000',42),
 ('respiratory_rate','ok','2001-03-02 malformed',42),
 ('respiratory_rate','ok','2001-03-02 27:00:00 +0000',42),
 ('respiratory_rate','ok','2001-03-17 08:00:00 +0000',null);
