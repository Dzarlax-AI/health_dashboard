-- A measured case per capture family in the combined graph, in addition to
-- the more extensive formula and range fixtures in the independent lanes.
insert into source_data.calendar values ('2026-02-01');
insert into source_data.metric_points values
  ('step_count','2026-02-01 09:00:00 +0000','Apple Watch',17,'ok');
insert into workout_capture.sessions values
  ('graph-fixture','w1','running','2026-02-01 09:00Z',3600,10,500,140,170,0,300,900,1800,600);
insert into workout_capture.periods values
  ('graph-fixture','p1','2026-02-01 00:00Z','2026-02-02 00:00Z','');
insert into energy_capture.runs values
  ('graph-fixture','e1','2026-02-01','retrospective_eod',3,0.90,1,true,0.1);
insert into energy_capture.days
  select 'graph-fixture','e1',date '2026-02-01'-n,7.5,1,1.5,0.2,450,1,'{}'
  from generate_series(0,30) n;
insert into energy_capture.decisions values
  ('graph-fixture','e1','2026-02-01',3,0.90,0.1,true,null,70,0,'{}','low','none');
insert into energy_capture.legacy values
  ('graph-fixture','e1',70,'normal',array[7.5],array[50.0],array[60.0],5000,6000,450,400);
insert into source_data.chip_target_input
select score || ':' || kind, score, date '2026-02-01', 35, 3, days, kind, date '2026-02-15'
from (values
  ('recovery_stability','rolling_3d'), ('recovery_stability','daily_point'),
  ('recovery_stability','rolling_3d_candidate_2of3'),
  ('passive_efficiency','rolling_3d'), ('passive_efficiency','daily_point'),
  ('acute_risk','event_t1_t3'), ('acute_risk','event_strict_t1_t3'),
  ('chronic_load','chronic_label'), ('chronic_load','chronic_acute_density')
) targets(score,kind)
cross join (select jsonb_agg(jsonb_build_object(
  'date',date '2026-02-01'+n,'value',0.8,'reason','ok','eligible',true,
  'observed',true,'z',0,'acute_observed',true,'acute_or',0,'hrv_z',0,'rhr_z',0
)) as days from generate_series(1,14) n) observations;
insert into source_data.chip_calibration_input values
  ('cal','acute_risk','event_t1_t3','fixture','2026-02-01');
insert into source_data.chip_display_input values
  ('shown','snapshot','acute_risk',0.25,0.5,'ok',null,false);
insert into source_data.chip_baseline_input values
  ('base','acute_risk','event_t1_t3','2026-02-01','2026-01-01',
   '[{"date":"2026-02-01","eligible":true,"value":0.2}]');
insert into source_data.chip_recovery_sleep_input values
  ('sleep','2026-02-01',null,'[]');
insert into briefing_pilot.architecture_captures values
  ('graph-fixture','a','2026-02-01','2026-02-01','');
insert into briefing_pilot.architecture_segments values
  ('graph-fixture','a','2026-02-01',1,'sleep_core','Apple Watch',0,21600000000000,6);
insert into briefing_pilot.oxygen_samples values
  ('graph-fixture',0,'Apple Watch',96,'ok');
insert into briefing_pilot.sri_captures values
  ('graph-fixture','s','2026-02-01',7,'');
insert into briefing_pilot.sri_segments values
  ('graph-fixture','s','2026-02-01 03:00','Apple Watch','sleep_core',3,'ok');
