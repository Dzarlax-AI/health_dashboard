package mcpserver

import (
	"context"
	"fmt"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"health-receiver/internal/ctxdb"
)

// dateRange parses YYYY-MM-DD strings into a [start-of-day, end-of-day] range
// in UTC. The DB stores start_time as TIMESTAMPTZ so the comparison works
// regardless of the original recording timezone — workouts that started on
// the requested day in any TZ are included.
func dateRange(fromStr, toStr string) (time.Time, time.Time, error) {
	from, err := time.Parse("2006-01-02", fromStr)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("from: %w", err)
	}
	to, err := time.Parse("2006-01-02", toStr)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("to: %w", err)
	}
	to = to.Add(24*time.Hour - time.Second)
	return from, to, nil
}

func registerWorkoutTools(s *server.MCPServer, _ DBResolver) {
	s.AddTool(mcp.NewTool("list_workout_types",
		mcp.WithDescription(`Discover what workout types (Apple Health workout activity names) are stored. Returns distinct names with counts and the date range each was last seen. Call this first when the user asks about a specific kind of training — Apple Health uses ~80 names (e.g. "Outdoor Run", "Indoor Cycling", "Traditional Strength Training", "Functional Strength Training", "Walking", "Hiking", "Yoga", "Tennis", "HIIT", "Rowing", "Swimming"). list_workouts and workout_stats accept any of these as the optional 'name' filter.`),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		types, err := ctxdb.FromContext(ctx).ListWorkoutTypes()
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return jsonResult(map[string]any{"count": len(types), "types": types})
	})

	s.AddTool(mcp.NewTool("list_workouts",
		mcp.WithDescription(`List Apple Health workouts (recorded on Apple Watch + iPhone) in a date range, sorted most-recent first. One row per workout with summary fields:

  external_id           — workout UUID (use with get_workout)
  name                  — activity type, e.g. "Outdoor Run" (call list_workout_types for the full set)
  start_time, end_time  — TIMESTAMPTZ
  duration_sec          — total seconds
  is_indoor, location   — environment
  avg_hr_bpm, max_hr_bpm — heart rate (NULL if no HR samples were recorded)
  energy_kcal           — active calories burned
  distance_km, avg_speed_kmh, max_speed_kmh — for runs / rides / walks
  elevation_up_m        — gross ascent (outdoor)
  step_count_total, step_cadence_spm — steps
  temperature_c, humidity_pct — outdoor weather
  intensity             — Apple intensity index (kcal/hr·kg)
  hr_z1_sec..hr_z5_sec  — time-in-HR-zone in seconds; Z1=recovery (lowest BPM),
                          Z5=VO2max (highest). Sum ≈ workout duration. NULL if
                          HR zones are not configured server-side or the
                          workout has no HR samples.

Per-second route polylines and per-minute HR sample arrays are NOT stored — this is for text analysis, not map rendering.`),
		mcp.WithString("from", mcp.Required(), mcp.Description("Start date YYYY-MM-DD (inclusive)")),
		mcp.WithString("to", mcp.Required(), mcp.Description("End date YYYY-MM-DD (inclusive)")),
		mcp.WithString("name", mcp.Description("Optional exact workout name filter. Use list_workout_types to discover the available names.")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		from, to, err := dateRange(req.GetString("from", ""), req.GetString("to", ""))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		name := req.GetString("name", "")
		workouts, err := ctxdb.FromContext(ctx).ListWorkouts(from, to, name)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return jsonResult(map[string]any{
			"from": from.Format("2006-01-02"), "to": to.Format("2006-01-02"),
			"name": name, "count": len(workouts), "workouts": workouts,
		})
	})

	s.AddTool(mcp.NewTool("get_workout",
		mcp.WithDescription("Fetch a single workout by its external_id (the HAE workout UUID returned in list_workouts)."),
		mcp.WithString("external_id", mcp.Required(), mcp.Description("Workout UUID, e.g. '323DE7A8-9785-4397-959A-4928CD389BD9'")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id := req.GetString("external_id", "")
		if id == "" {
			return mcp.NewToolResultError("external_id is required"), nil
		}
		w, err := ctxdb.FromContext(ctx).GetWorkout(id)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if w == nil {
			return mcp.NewToolResultText("workout not found"), nil
		}
		return jsonResult(w)
	})

	s.AddTool(mcp.NewTool("workout_stats",
		mcp.WithDescription(`Aggregate counters for workouts in a date range. Returns:

  count              — number of workouts in window
  total_duration_sec — sum of durations across all matched workouts
  total_distance_km  — sum of distance (NULL if no distance-bearing workouts matched)
  total_energy_kcal  — sum of active energy burned
  avg_hr_bpm         — unweighted mean of per-workout average HR
  max_hr_bpm         — max HR across all matched workouts
  total_hr_z1_sec..total_hr_z5_sec — total time-in-HR-zone seconds. Z1=recovery
                       (lowest BPM), Z5=VO2max. Sum across zones ≈ total HR-tracked time.

Use this for "how much did I run last month", "how much time in Z4 last week", or "total kcal burned in cycling sessions this year" questions. Combine with list_workout_types for discovery before filtering by name.`),
		mcp.WithString("from", mcp.Required(), mcp.Description("Start date YYYY-MM-DD (inclusive)")),
		mcp.WithString("to", mcp.Required(), mcp.Description("End date YYYY-MM-DD (inclusive)")),
		mcp.WithString("name", mcp.Description("Optional exact workout name filter. Use list_workout_types to discover the available names.")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		from, to, err := dateRange(req.GetString("from", ""), req.GetString("to", ""))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		name := req.GetString("name", "")
		stats, err := ctxdb.FromContext(ctx).WorkoutStats(from, to, name)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return jsonResult(map[string]any{
			"from": from.Format("2006-01-02"), "to": to.Format("2006-01-02"),
			"name": name, "stats": stats,
		})
	})
}
