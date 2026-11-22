create schema source_data;
create table source_data.sri_captures(
    profile text, capture text, anchor date, window_days int, preferred_source text, primary key(profile,capture)
);
create table source_data.sri_segments(
    profile text, capture text, observed_at timestamp, source text, metric_name text, qty float8, quality text
);
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
