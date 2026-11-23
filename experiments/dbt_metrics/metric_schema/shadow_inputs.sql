create schema sleep_shadow;
create table sleep_shadow.candidate (
    profile text, wake_date date, id text, revision integer, priority integer, snapshot jsonb,
    primary key(profile,wake_date,id)
);
insert into sleep_shadow.candidate values
    ('shadow-fixture','2026-01-15','watch',1,0,'{"zero_stages":["awake"],"stages":[{"kind":"core","start_at":"2026-01-15T00:00Z","end_at":"2026-01-15T06:00Z"}]}');
grant usage on schema sleep_shadow to shadow_reader,shadow_dbt;
grant select on sleep_shadow.candidate to shadow_reader,shadow_dbt;
insert into source_data.hrv_point values
    ('2026-01-15 08:00:00 +0000','RMSSD fixture',41,'ok','ms','rmssd','synthetic','timestamp','2026-01-15 08:00Z',null);

-- An interval wholly inside the separate post-workout window.
insert into source_data.hrv_point
select to_char(end_at + interval '5 minutes','YYYY-MM-DD HH24:MI:SS OF'),
       'interval fixture',52,'ok','ms','sdnn','synthetic','interval',
       end_at + interval '5 minutes', end_at + interval '6 minutes'
from source_data.workout order by start_at limit 1;
