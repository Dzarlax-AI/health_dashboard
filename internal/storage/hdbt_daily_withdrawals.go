package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

var hdbtDailyWithdrawalMetricNames = []string{
	"heart_rate_variability", "resting_heart_rate", "active_energy", "apple_exercise_time",
	"blood_oxygen_saturation", "oxygen_saturation", "vo2_max", "respiratory_rate",
	"sleep_total", "sleep_deep", "sleep_rem", "sleep_core", "sleep_awake", "sleep_unspecified",
}

var hdbtDailyWithdrawalMetrics = "('" + strings.Join(hdbtDailyWithdrawalMetricNames, "','") + "')"

type dailyPointRepairs struct {
	hrvDates    []string
	withdrawals [][]byte
}

func previousPositiveDailyPoints(ctx context.Context, tx pgx.Tx, points []MetricPoint) (json.RawMessage, error) {
	var keys [][3]string
	for _, point := range points {
		if float32(point.Qty) > 0 {
			continue
		}
		point = normalizeStoredMetricPoint(point)
		if float32(point.Qty) <= 0 && slices.Contains(hdbtDailyWithdrawalMetricNames, point.MetricName) {
			keys = append(keys, [3]string{point.MetricName, point.Date, point.Source})
		}
	}
	if len(keys) == 0 {
		return nil, nil
	}
	payload, err := json.Marshal(keys)
	if err != nil {
		return nil, err
	}
	var previous json.RawMessage
	err = tx.QueryRow(ctx, `select jsonb_agg(distinct key)
		from jsonb_array_elements($1::jsonb) key
		join metric_points p on (p.metric_name,p.date,p.source)=(key->>0,key->>1,key->>2)
		where p.quality='ok' and p.qty>0`, json.RawMessage(payload)).Scan(&previous)
	return previous, err
}

func acceptedLiveDailyWithdrawals(ctx context.Context, tx pgx.Tx, recordID int64, previous json.RawMessage) (dailyPointRepairs, error) {
	if len(previous) == 0 {
		return dailyPointRepairs{}, nil
	}
	var repairs dailyPointRepairs
	var withdrawals []byte
	err := tx.QueryRow(ctx, `select array_agg(distinct substring(p.date,1,10))
			filter(where p.metric_name='heart_rate_variability'),
			jsonb_agg(distinct jsonb_build_array(substring(p.date,1,10),p.metric_name))
		from jsonb_array_elements($1::jsonb) key
		join metric_points p on (p.metric_name,p.date,p.source)=(key->>0,key->>1,key->>2)
		where p.health_record_id=$2 and p.qty<=0`, previous, recordID).Scan(&repairs.hrvDates, &withdrawals)
	if len(withdrawals) != 0 {
		repairs.withdrawals = [][]byte{withdrawals}
	}
	return repairs, err
}

// Capture explicit removals of usable inputs separately from ordinary dirty dates.
// A missing fresh value alone does not invalidate a retained aggregate.
func recordHDBTDailyWithdrawals(ctx context.Context, tx pgx.Tx, generation uint64, sets ...[]byte) error {
	var groups []json.RawMessage
	for _, set := range sets {
		if len(set) != 0 {
			groups = append(groups, json.RawMessage(set))
		}
	}
	if len(groups) == 0 {
		return nil
	}
	var installed bool
	if err := tx.QueryRow(ctx, `select to_regclass(format('%I.hdbt_daily_withdrawals',current_schema())) is not null`).Scan(&installed); err != nil {
		return err
	}
	if !installed {
		return nil
	}
	payload, err := json.Marshal(groups)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `insert into hdbt_daily_withdrawals(date,metric_name,generation)
		select distinct entry->>0,entry->>1,$2::text::numeric
		from jsonb_array_elements($1::jsonb) g, jsonb_array_elements(g) entry
		where entry->>1 in `+hdbtDailyWithdrawalMetrics+`
		order by 1,2
		on conflict(date,metric_name) do update set generation=excluded.generation`,
		json.RawMessage(payload), strconv.FormatUint(generation, 10))
	if err != nil {
		return fmt.Errorf("record aggregate withdrawals: %w", err)
	}
	return nil
}
