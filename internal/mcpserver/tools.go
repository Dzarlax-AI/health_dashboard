package mcpserver

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"health-receiver/internal/ctxdb"
)

func registerMetricTools(s *server.MCPServer, _ DBResolver) {
	s.AddTool(mcp.NewTool("get_health_briefing",
		mcp.WithDescription("Get a full daily health briefing with readiness score, server-owned readiness_serving freshness/confidence metadata, sleep analysis, recovery, activity, cardio sections, EnergyBank, insights, and health alerts. Best starting point for consumers that must not guess missing/stale/low-coverage states."),
		mcp.WithString("lang", mcp.Description("Response language: en, ru, sr (default: en)")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		lang := req.GetString("lang", "en")
		briefing, err := ctxdb.FromContext(ctx).GetHealthBriefing(lang)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return jsonResult(briefing)
	})

	s.AddTool(mcp.NewTool("get_ai_briefing",
		mcp.WithDescription("Get the AI-assisted morning wellness explanation for a given date. The server computes the verdict and health facts; the active AI provider produces one concise SYNTHESIS explanation. Legacy dates may still contain the older SLEEP, YESTERDAY, RECOVERY, and RECOMMENDATION format. Returns empty if no briefing was generated."),
		mcp.WithString("date", mcp.Description("Date YYYY-MM-DD (default: today)")),
		mcp.WithString("lang", mcp.Description("Language: en, ru, sr (default: en). Returns cached briefing matching this language.")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		db := ctxdb.FromContext(ctx)
		date := req.GetString("date", db.Today())
		lang := req.GetString("lang", "en")
		insight := db.GetAIInsightCombined(date, lang)
		if insight == "" {
			return mcp.NewToolResultText("No AI briefing available for " + date), nil
		}
		return jsonResult(map[string]string{"date": date, "lang": lang, "insight": insight})
	})

	s.AddTool(mcp.NewTool("get_readiness_history",
		mcp.WithDescription("Get daily composite readiness scores (0–100) and server-derived readiness bands for the last N days. For today's freshness/confidence contract, use get_health_briefing and read readiness_serving."),
		mcp.WithNumber("days", mcp.Description("Number of recent days (default: 30)")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		days := req.GetInt("days", 30)
		pts, err := ctxdb.FromContext(ctx).GetReadinessHistory(days)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return jsonResult(map[string]any{"days": days, "points": pts})
	})

	s.AddTool(mcp.NewTool("list_metrics",
		mcp.WithDescription("List all available health metrics with record counts and date ranges. Call this first to discover what data is available."),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		metrics, err := ctxdb.FromContext(ctx).ListMetrics()
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return jsonResult(metrics)
	})

	s.AddTool(mcp.NewTool("get_dashboard",
		mcp.WithDescription("Get today's health summary: steps, calories, heart rate, SpO2, HRV, sleep and more. Includes trend vs yesterday."),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		cards, err := ctxdb.FromContext(ctx).GetDashboard()
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return jsonResult(cards)
	})

	s.AddTool(mcp.NewTool("get_metric_data",
		mcp.WithDescription("Get time series data for a single health metric. Use bucket='day' for trends, 'hour' for intraday patterns, 'minute' for raw resolution."),
		mcp.WithString("metric", mcp.Required(), mcp.Description("Metric name, e.g. heart_rate, step_count, sleep_total")),
		mcp.WithString("from", mcp.Required(), mcp.Description("Start date YYYY-MM-DD")),
		mcp.WithString("to", mcp.Required(), mcp.Description("End date YYYY-MM-DD")),
		mcp.WithString("bucket", mcp.Description("minute, hour, day (default: day)")),
		mcp.WithString("agg", mcp.Description("AVG, SUM, MAX, MIN (default: auto based on metric type)")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		metric := req.GetString("metric", "")
		from := req.GetString("from", "")
		to := req.GetString("to", "")
		bucket := req.GetString("bucket", "day")
		aggFunc := req.GetString("agg", "")
		if aggFunc == "" {
			if sumMetrics[metric] {
				aggFunc = "SUM"
			} else {
				aggFunc = "AVG"
			}
		}
		points, err := ctxdb.FromContext(ctx).GetMetricData(metric, from, to+" 23:59:59", bucket, aggFunc)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return jsonResult(map[string]any{"metric": metric, "bucket": bucket, "agg": aggFunc, "count": len(points), "points": points})
	})

	s.AddTool(mcp.NewTool("summarize_metric",
		mcp.WithDescription("Statistical summary for a metric over recent days: avg, min, max, count + daily breakdown. Good for quick trend analysis."),
		mcp.WithString("metric", mcp.Required(), mcp.Description("Metric name, e.g. heart_rate, step_count")),
		mcp.WithNumber("days", mcp.Description("Number of recent days (default: 7)")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		metric := req.GetString("metric", "")
		days := req.GetInt("days", 7)
		stats, err := ctxdb.FromContext(ctx).SummarizeMetric(metric, days)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return jsonResult(stats)
	})

}

func registerAnalysisTools(s *server.MCPServer, _ DBResolver) {
	s.AddTool(mcp.NewTool("compare_periods",
		mcp.WithDescription("Compare a metric between two date ranges. Useful for before/after analysis or week-over-week comparisons."),
		mcp.WithString("metric", mcp.Required(), mcp.Description("Metric name, e.g. heart_rate, step_count")),
		mcp.WithString("period_a_from", mcp.Required(), mcp.Description("Period A start date YYYY-MM-DD")),
		mcp.WithString("period_a_to", mcp.Required(), mcp.Description("Period A end date YYYY-MM-DD")),
		mcp.WithString("period_b_from", mcp.Required(), mcp.Description("Period B start date YYYY-MM-DD")),
		mcp.WithString("period_b_to", mcp.Required(), mcp.Description("Period B end date YYYY-MM-DD")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		metric := req.GetString("metric", "")
		aFromStr := req.GetString("period_a_from", "")
		aToStr := req.GetString("period_a_to", "")
		bFromStr := req.GetString("period_b_from", "")
		bToStr := req.GetString("period_b_to", "")

		agg := "AVG"
		if sumMetrics[metric] {
			agg = "SUM"
		}

		type periodStats struct {
			From       string  `json:"from"`
			To         string  `json:"to"`
			Value      float64 `json:"value"`
			Min        float64 `json:"min"`
			Max        float64 `json:"max"`
			DataPoints int     `json:"data_points"`
		}

		// Use GetMetricData (day bucket) which handles multi-device dedup
		// correctly for SUM metrics, then aggregate daily values.
		queryPeriod := func(from, to string) (periodStats, error) {
			points, err := ctxdb.FromContext(ctx).GetMetricData(metric, from, to+" 23:59:59", "day", agg)
			if err != nil {
				return periodStats{From: from, To: to}, err
			}
			ps := periodStats{From: from, To: to, DataPoints: len(points)}
			if len(points) == 0 {
				return ps, nil
			}
			sum := 0.0
			ps.Min = points[0].Qty
			ps.Max = points[0].Qty
			for _, p := range points {
				sum += p.Qty
				if p.Qty < ps.Min {
					ps.Min = p.Qty
				}
				if p.Qty > ps.Max {
					ps.Max = p.Qty
				}
			}
			if sumMetrics[metric] {
				ps.Value = sum // total over period
			} else {
				ps.Value = sum / float64(len(points)) // average over period
			}
			return ps, nil
		}

		a, err := queryPeriod(aFromStr, aToStr)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		b, err := queryPeriod(bFromStr, bToStr)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		var changePct *float64
		if a.Value != 0 {
			pct := (b.Value - a.Value) / a.Value * 100
			changePct = &pct
		}

		return jsonResult(map[string]any{
			"metric": metric, "agg": agg,
			"period_a": a, "period_b": b, "change_pct": changePct,
		})
	})

	s.AddTool(mcp.NewTool("get_sleep_summary",
		mcp.WithDescription("Get sleep breakdown by phase (deep, REM, core, unspecified, awake, total) per night for a date range. `unspecified` is coarse asleep time from sources without per-stage tracking (RingConn, iPhone Sleep Schedule, older Apple Watch) and is 0 on Apple Watch nights with stage data. Values are deduplicated across devices."),
		mcp.WithString("from", mcp.Required(), mcp.Description("Start date YYYY-MM-DD")),
		mcp.WithString("to", mcp.Required(), mcp.Description("End date YYYY-MM-DD")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		from := req.GetString("from", "")
		to := req.GetString("to", "")
		nights, err := ctxdb.FromContext(ctx).GetSleepSummary(from, to)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return jsonResult(map[string]any{"from": from, "to": to, "nights": nights})
	})

	s.AddTool(mcp.NewTool("find_anomalies",
		mcp.WithDescription("Find days where a metric was statistically unusual (beyond mean ± sigma threshold). Useful for spotting illness, overtraining, or unusually good days."),
		mcp.WithString("metric", mcp.Required(), mcp.Description("Metric name, e.g. heart_rate, hrv, step_count")),
		mcp.WithString("from", mcp.Required(), mcp.Description("Start date YYYY-MM-DD")),
		mcp.WithString("to", mcp.Required(), mcp.Description("End date YYYY-MM-DD")),
		mcp.WithNumber("sigma", mcp.Description("Threshold in standard deviations (default: 2.0)")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		metric := req.GetString("metric", "")
		from := req.GetString("from", "")
		to := req.GetString("to", "")
		sigma := req.GetFloat("sigma", 2.0)

		agg := "AVG"
		if sumMetrics[metric] {
			agg = "SUM"
		}
		points, err := ctxdb.FromContext(ctx).GetMetricData(metric, from, to+" 23:59:59", "day", agg)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if len(points) < 3 {
			return mcp.NewToolResultText("not enough data points to detect anomalies"), nil
		}

		var sum float64
		for _, p := range points {
			sum += p.Qty
		}
		mean := sum / float64(len(points))

		var variance float64
		for _, p := range points {
			d := p.Qty - mean
			variance += d * d
		}
		stddev := math.Sqrt(variance / float64(len(points)))

		type anomaly struct {
			Date   string  `json:"date"`
			Value  float64 `json:"value"`
			ZScore float64 `json:"z_score"`
			Type   string  `json:"type"`
		}
		var anomalies []anomaly
		for _, p := range points {
			z := (p.Qty - mean) / stddev
			if math.Abs(z) >= sigma {
				t := "high"
				if z < 0 {
					t = "low"
				}
				anomalies = append(anomalies, anomaly{Date: p.Date, Value: p.Qty, ZScore: math.Round(z*100) / 100, Type: t})
			}
		}

		return jsonResult(map[string]any{
			"metric": metric, "from": from, "to": to,
			"mean": math.Round(mean*100) / 100, "stddev": math.Round(stddev*100) / 100,
			"sigma": sigma, "anomalies": anomalies,
		})
	})

	s.AddTool(mcp.NewTool("get_weekly_summary",
		mcp.WithDescription("Weekly aggregates for one or more metrics. Good for spotting week-over-week trends."),
		mcp.WithString("metrics", mcp.Required(), mcp.Description("Comma-separated metric names, e.g. 'step_count,heart_rate,sleep_total'")),
		mcp.WithString("from", mcp.Required(), mcp.Description("Start date YYYY-MM-DD")),
		mcp.WithString("to", mcp.Required(), mcp.Description("End date YYYY-MM-DD")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		metricsStr := req.GetString("metrics", "")
		from := req.GetString("from", "")
		to := req.GetString("to", "")

		metricList := strings.Split(metricsStr, ",")
		result := map[string]any{}

		for _, metric := range metricList {
			metric = strings.TrimSpace(metric)
			if metric == "" {
				continue
			}
			agg := "AVG"
			if sumMetrics[metric] {
				agg = "SUM"
			}
			// Use GetMetricData which handles multi-device dedup,
			// then aggregate daily values into weeks in Go.
			points, err := ctxdb.FromContext(ctx).GetMetricData(metric, from, to+" 23:59:59", "day", agg)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("%s: %v", metric, err)), nil
			}
			type weekData struct {
				Week       string  `json:"week"`
				Value      float64 `json:"value"`
				DataPoints int     `json:"data_points"`
			}
			weekMap := map[string]*weekData{}
			var weekOrder []string
			for _, p := range points {
				if len(p.Date) < 10 {
					continue
				}
				// Compute ISO week from date string.
				t, perr := time.Parse("2006-01-02", p.Date[:10])
				if perr != nil {
					continue
				}
				y, w := t.ISOWeek()
				wk := fmt.Sprintf("%d-W%02d", y, w)
				wd, ok := weekMap[wk]
				if !ok {
					wd = &weekData{Week: wk}
					weekMap[wk] = wd
					weekOrder = append(weekOrder, wk)
				}
				wd.Value += p.Qty
				wd.DataPoints++
			}
			// For AVG metrics, convert accumulated sum to average.
			if !sumMetrics[metric] {
				for _, wd := range weekMap {
					if wd.DataPoints > 0 {
						wd.Value /= float64(wd.DataPoints)
					}
				}
			}
			var weeks []weekData
			for _, wk := range weekOrder {
				w := weekMap[wk]
				w.Value = math.Round(w.Value*100) / 100
				weeks = append(weeks, *w)
			}
			result[metric] = map[string]any{"agg": agg, "weeks": weeks}
		}
		return jsonResult(map[string]any{"from": from, "to": to, "data": result})
	})

	s.AddTool(mcp.NewTool("get_personal_records",
		mcp.WithDescription("Best (max) and worst (min) daily values ever recorded for each metric, with the date they occurred. SUM metrics show best/worst daily totals (with multi-device dedup); AVG metrics show best/worst daily averages."),
		mcp.WithString("metric", mcp.Description("Filter to a single metric. If omitted, returns records for all metrics.")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		metric := req.GetString("metric", "")

		// Get list of metrics to process.
		metrics, err := ctxdb.FromContext(ctx).ListMetrics()
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		type record struct {
			Metric     string  `json:"metric"`
			BestValue  float64 `json:"best_value"`
			BestDate   string  `json:"best_date"`
			WorstValue float64 `json:"worst_value"`
			WorstDate  string  `json:"worst_date"`
		}
		var records []record

		for _, m := range metrics {
			if metric != "" && m.Name != metric {
				continue
			}
			// Use GetMetricData with day bucket — handles dedup correctly.
			minDate, maxDate, rerr := ctxdb.FromContext(ctx).GetMetricDateRange(m.Name)
			if rerr != nil || minDate == "" {
				continue
			}
			agg := "AVG"
			if sumMetrics[m.Name] {
				agg = "SUM"
			}
			points, derr := ctxdb.FromContext(ctx).GetMetricData(m.Name, minDate, maxDate+" 23:59:59", "day", agg)
			if derr != nil || len(points) == 0 {
				continue
			}
			rec := record{Metric: m.Name, BestValue: points[0].Qty, WorstValue: points[0].Qty, BestDate: points[0].Date, WorstDate: points[0].Date}
			for _, p := range points[1:] {
				if p.Qty > rec.BestValue {
					rec.BestValue = p.Qty
					rec.BestDate = p.Date
				}
				if p.Qty < rec.WorstValue {
					rec.WorstValue = p.Qty
					rec.WorstDate = p.Date
				}
			}
			records = append(records, rec)
		}
		return jsonResult(records)
	})
}
