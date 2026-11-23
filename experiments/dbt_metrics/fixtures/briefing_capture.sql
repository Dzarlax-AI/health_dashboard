create schema briefing_pilot; create table briefing_pilot.captured_inputs(profile text,capture int,inputs jsonb,primary key(profile,capture));
create table briefing_pilot.sri_captures(profile text,capture text,anchor date,window_days int,preferred_source text,primary key(profile,capture));
create table briefing_pilot.architecture_captures(profile text,capture text,from_day date,to_day date,preferred_source text);
create table briefing_pilot.architecture_segments(profile text,capture text,day text,ordinal bigint,metric text,source text,start_ns bigint,end_ns bigint,hours float8);
create table briefing_pilot.oxygen_samples(profile text,capture int,source text,qty float8,quality text);
create table briefing_pilot.sri_segments(profile text,capture text,observed_at timestamp,source text,metric_name text,qty float8,quality text);
