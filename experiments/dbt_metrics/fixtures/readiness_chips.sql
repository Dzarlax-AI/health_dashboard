CREATE TABLE source_data.chip_target_input (
 case_id text PRIMARY KEY, sub_score text NOT NULL, target_date date NOT NULL,
 paired_count integer, breach_threshold integer, days jsonb NOT NULL, target_kind text NOT NULL, latest_sleep date
);
CREATE TABLE source_data.chip_calibration_input (
 case_id text PRIMARY KEY, sub_score text NOT NULL, target_kind text NOT NULL,
 epoch text NOT NULL, as_of_date date NOT NULL
);
CREATE TABLE source_data.chip_pairs (
 case_id text NOT NULL REFERENCES source_data.chip_calibration_input, day date NOT NULL,
 prediction_epoch text, label_epoch text, baseline_kind text, predicted real, label real, eligible boolean,
 PRIMARY KEY (case_id, day)
);
CREATE TABLE source_data.chip_display_input (
 case_id text PRIMARY KEY, capture_mode text NOT NULL, sub_score text NOT NULL,
 predicted double precision, cutoff double precision, calibration_status text,
 baseline_reason text, epoch_changed boolean NOT NULL
);
GRANT SELECT ON source_data.chip_target_input, source_data.chip_calibration_input,
 source_data.chip_pairs, source_data.chip_display_input TO pilot_dbt;

CREATE TABLE source_data.chip_baseline_input (
 case_id text PRIMARY KEY, sub_score text NOT NULL, target_kind text NOT NULL,
 as_of_date date NOT NULL, epoch_start date, days jsonb NOT NULL
);
GRANT SELECT ON source_data.chip_baseline_input TO pilot_dbt;

CREATE TABLE source_data.chip_recovery_sleep_input (
 case_id text PRIMARY KEY, as_of_date date NOT NULL, epoch_start date, days jsonb
);
GRANT SELECT ON source_data.chip_recovery_sleep_input TO pilot_dbt;
