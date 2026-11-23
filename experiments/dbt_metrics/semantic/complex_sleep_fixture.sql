-- A capture is a frozen synthetic input at one decision time.
create table source_data.balance_capture(profile text, capture text, wake_date date not null, zone text not null, primary key(profile,capture));
create table source_data.balance_goal(profile text, capture text, effective_date date, hours float8 not null check(hours between 3 and 14), primary key(profile,capture,effective_date));
create table source_data.balance_coverage(profile text, capture text, period_day date, state text not null, primary key(profile,capture,period_day));
create table source_data.balance_episode(profile text, capture text, id text, start_at timestamptz not null, end_at timestamptz not null,
  assessment text not null, primary key(profile,capture,id), check(end_at>start_at));
insert into source_data.balance_capture
select 'one',v,date '2026-03-30','Europe/Belgrade' from unnest(array['initial','corrected','deleted','partial','unknown','missing','invalid','goal_missing','outlier','zero','partial_after_outlier']) v
union all select 'two','initial',date '2026-03-30','Europe/Belgrade';
insert into source_data.balance_goal
select profile,capture,wake_date-20,case when profile='two' then 8 else 7.5 end from source_data.balance_capture where capture<>'goal_missing'
union all select profile,capture,wake_date-5,8.25 from source_data.balance_capture;
insert into source_data.balance_coverage
select profile,capture,wake_date-n,
       case when capture in ('partial','partial_after_outlier') and n=3 then 'partial'
            when capture='unknown' and n=8 then 'unknown'
            when capture='invalid' and n=7 then 'other'
            else 'complete' end
from source_data.balance_capture cross join generate_series(0,13) n
where not(capture='missing' and n=5);
insert into source_data.balance_episode
select profile,capture,n::text,((wake_date-n-1)+time '22:00') at time zone zone,
       ((wake_date-n)+case when capture='corrected' and n=1 then time '05:30' else time '06:00' end) at time zone zone,
       case when capture in ('outlier','partial_after_outlier') and n=12 then 'outlier' else 'plausible' end
from source_data.balance_capture cross join generate_series(0,13) n
where capture<>'zero' and not(capture='deleted' and n=1);
-- One nap straddles local noon and must be split across adjacent periods.
insert into source_data.balance_episode select profile,capture,'nap',
  ((wake_date-2)+time '11:50') at time zone zone,((wake_date-2)+time '12:20') at time zone zone,'plausible'
from source_data.balance_capture where capture<>'zero';

-- Fractional episodes expose rounding before the daily sum.
insert into source_data.balance_episode
select profile,capture,'fragment-'||n,(wake_date+start_time) at time zone zone,
       ((wake_date+start_time) at time zone zone)+duration,'plausible'
from source_data.balance_capture cross join (values
    (1,time '07:00',interval '18.849953 seconds'),
    (2,time '08:00',interval '32.573510 seconds'),
    (3,time '09:00',interval '3.179705 seconds')
) as fragments(n,start_time,duration)
where profile='one' and capture='initial';

create table source_data.wake_capture(profile text, capture text,zone text not null,decision_at timestamptz not null,
 candidate_wake timestamptz,latest_ingest timestamptz,quiet_since timestamptz, signal text,steps float8 not null, typical_minute integer,primary key(profile,capture));
insert into source_data.wake_capture
select p, label,'Europe/Belgrade',wake_at+age*interval '1 minute',case when label<>'no_data' then wake_at end,
 wake_at+age*interval '1 minute'-receipt_age*interval '1 minute',
 wake_at+age*interval '1 minute'-quiet_age*interval '1 minute',signal,steps,typical
from (values
 ('one','no_data',0,0,0,'detailed_stage_end',0,null::int),
 ('one','future',-1,60,60,'detailed_stage_end',200,null),
 ('one','29_minutes',29,60,60,'detailed_stage_end',200,null),
 ('one','activity_threshold',30,20,20,'detailed_stage_end',100,null),
 ('two','activity_threshold',30,20,20,'detailed_stage_end',99,null),
 ('one','writing',70,1,19,'detailed_stage_end',200,null),
 ('one','same_receipt',70,1,20,'detailed_stage_end',200,null),
 ('one','no_clock',70,1,null,'detailed_stage_end',200,null),
 ('one','fallback_active',30,30,30,'midnight_summary',100,null),
 ('one','fallback_89',89,90,90,'midnight_summary',0,null),
 ('one','fallback_90',90,90,90,'midnight_summary',0,null),
 ('one','passive_59',59,60,60,'detailed_stage_end',0,null),
 ('one','passive_60',60,60,60,'detailed_stage_end',0,null),
 ('one','early',80,100,100,'detailed_stage_end',0,600),
 ('one','early_timeout',90,100,100,'detailed_stage_end',0,600),
 ('one','missing_typical',85,100,100,'detailed_stage_end',0,null)
) f(p,label,age,receipt_age,quiet_age,signal,steps,typical)
cross join (values (timestamptz '2026-03-29 06:00:00+02')) w(wake_at);

create table source_data.wake_segment_case(profile text,capture text,target_day date not null,zone text not null, primary key(profile,capture));
create table source_data.wake_segment(profile text,capture text,writer_day date,source text,metric text,start_at timestamptz,hours float8,
 received_at timestamptz,quality text);
insert into source_data.wake_segment_case select 'one',c,date '2026-03-30','Europe/Belgrade'
from unnest(array['stage','raw','midnight','empty','outside','tie','minimum','corrected','withdrawn']) c;
insert into source_data.wake_segment_case values('two','stage','2026-03-30','Europe/Belgrade');
insert into source_data.wake_segment
select profile,capture,local_start::date,source,metric,local_start at time zone zone,hours,
  (timestamp '2026-03-30 10:00' at time zone zone)+received_delta*interval '1 minute',quality
from (values
 ('one','stage','Apple Watch','sleep_core',timestamp '2026-03-30 03:00',2.,1,'ok'),
 ('one','stage','Apple Watch','sleep_awake','2026-03-30 05:00',0.5,3,'ok'),
 ('one','stage','RingConn','sleep_core','2026-03-30 02:30',6,7,'ok'),
 ('two','stage','RingConn','sleep_rem','2026-03-30 06:00',1,1,'ok'),
 ('one','raw','watch','sleep_total','2026-03-29 23:14',7.5,5,'ok'),
 ('one','midnight','RingConn','sleep_total','2026-03-30 00:00',7,3,'ok'),
 ('one','midnight','RingConn','sleep_awake','2026-03-30 00:00',0.75,4,'ok'),
 ('one','outside','watch','sleep_core','2026-03-30 15:00',1,1,'ok'),
 ('one','tie','Z Apple Watch','sleep_core','2026-03-30 03:45',3,1,'ok'),
 ('one','tie','Ä Apple Watch','sleep_core','2026-03-30 04:00',3,1,'ok'),
 ('one','minimum','Apple Watch','sleep_core','2026-03-30 04:00',1,1,'ok'),
 ('one','minimum','RingConn','sleep_core','2026-03-30 05:00',2,2,'ok'),
 ('one','corrected','Apple Watch','sleep_core','2026-03-30 03:00',3,8,'ok'),
 ('one','withdrawn','Apple Watch','sleep_core','2026-03-30 03:00',2,1,'invalid')
) f(profile,capture,source,metric,local_start,hours,received_delta,quality)
join source_data.wake_segment_case using (profile,capture);

alter table source_data.wake_segment_case add column preferred_source text;
insert into source_data.wake_segment_case(profile,capture,target_day,zone,preferred_source)
select 'one',capture,date '2026-03-30','Europe/Belgrade',preference
from (values ('prefer_ring','RingConn'),('thin','Apple Watch'),('missing_pref','watch'),('quality_first','RingConn'),
             ('prefer_midnight','RingConn'),('invalid_midnight','RingConn')) f(capture,preference);
insert into source_data.wake_segment
select s.profile,f.capture,s.writer_day,s.source,s.metric,s.start_at,s.hours,s.received_at,s.quality
from source_data.wake_segment s join (values ('prefer_ring','stage'),('thin','minimum'),('missing_pref','tie'),
    ('quality_first','stage'),('prefer_midnight','midnight'),('invalid_midnight','midnight')) f(capture,original)
on s.profile='one' and s.capture=f.original;
update source_data.wake_segment set metric='sleep_total' where capture='quality_first' and source='RingConn';
update source_data.wake_segment set hours=17 where capture='invalid_midnight' and metric='sleep_total';
insert into source_data.wake_segment
select profile,capture,writer_day,'Apple Watch',metric,start_at,6,received_at,quality
from source_data.wake_segment where capture in ('prefer_midnight','invalid_midnight') and metric='sleep_total';

-- An invalid high-priority summary must not hide usable data when a
-- preference is configured. The same inputs also pin legacy automatic mode.
insert into source_data.wake_segment_case(profile,capture,target_day,zone,preferred_source)
values ('one','invalid_watch','2026-03-30','Europe/Belgrade','Apple Watch'),
       ('one','absent_watch','2026-03-30','Europe/Belgrade','Unknown'),
       ('one','automatic_invalid_watch','2026-03-30','Europe/Belgrade','');
insert into source_data.wake_segment
select profile,capture,target_day,source,'sleep_total',target_day::timestamp at time zone zone,
       hours,(target_day + time '18:00') at time zone zone,'ok'
from source_data.wake_segment_case cross join (values ('Apple Watch',17),('RingConn',7)) s(source,hours)
where capture in ('invalid_watch','absent_watch','automatic_invalid_watch');

CREATE TABLE source_data.sleep_rollout_daily (
    wake_date date PRIMARY KEY, generation bigint, candidate text, revision bigint,
    sleep_seconds double precision, episode_selection jsonb
);
