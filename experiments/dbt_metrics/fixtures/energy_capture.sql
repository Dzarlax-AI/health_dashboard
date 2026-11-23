create schema energy_capture;
create table energy_capture.runs (
  profile text not null, capture_id text not null, target_day date not null,
  snapshot_kind text not null check (snapshot_kind in ('observed_intraday','retrospective_eod')),
  formula_version int not null check (formula_version=3), alpha float8 not null, factor float8 not null,
  stress_enabled boolean not null, beta float8 not null, primary key(profile,capture_id)
);
create table energy_capture.days (
  profile text not null, capture_id text not null, day date not null,
  sleep_total float8, deep float8, rem float8, awake float8,
  active_kcal float8, hr_load float8, stress_flags text[],
  primary key(profile,capture_id,day), foreign key(profile,capture_id) references energy_capture.runs
);
create table energy_capture.decisions (
  profile text not null, capture_id text not null, through_day date not null,
  formula_version int not null, alpha_effective float8 not null, beta_effective float8 not null,
  stress_enabled boolean not null, settings_changed timestamptz,
  display_bank int not null, hrv_z float8 not null, flags text[] not null,
  confidence text not null, pattern text not null, primary key(profile,capture_id)
);
create table energy_capture.band_history (
  profile text not null, capture_id text not null, day date not null, ts_bucket timestamptz not null,
  computed_at timestamptz not null, formula_version int not null, bank int not null, flags text[] not null,
  primary key(profile,capture_id,ts_bucket), foreign key(profile,capture_id) references energy_capture.decisions
);
create table energy_capture.legacy (
  profile text not null, capture_id text not null, readiness int not null, headline text not null,
  sleep float8[] not null, hrv float8[] not null, rhr float8[] not null, steps float8 not null,
  usual_steps float8 not null, kcal float8 not null, usual_kcal float8 not null, primary key(profile,capture_id)
);
grant usage on schema energy_capture to pilot_dbt;
grant select on all tables in schema energy_capture to pilot_dbt;
