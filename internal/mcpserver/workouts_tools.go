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
	s.AddTool(mcp.NewTool("list_workouts",
		mcp.WithDescription(`List Apple Health workouts (recorded on Apple Watch + iPhone) in a date range, sorted most-recent first. One row per workout with summary fields (duration, distance, energy, heart rate, time-in-HR-zone). Per-second route / HR / energy timeseries are not stored — use this for analysis, not for plotting maps.`),
		mcp.WithString("from", mcp.Required(), mcp.Description("Start date YYYY-MM-DD (inclusive)")),
		mcp.WithString("to", mcp.Required(), mcp.Description("End date YYYY-MM-DD (inclusive)")),
		mcp.WithString("name", mcp.Description("Optional exact workout name filter, e.g. 'Outdoor Run', 'Indoor Cycling', 'Traditional Strength Training'")),
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
		mcp.WithDescription(`Aggregate counters for workouts in a date range: total count, total duration, total distance, total energy, average / max heart rate, and total time-in-HR-zone seconds. Use this for "how much did I run last month" or "how much time in Z4 last week" questions.`),
		mcp.WithString("from", mcp.Required(), mcp.Description("Start date YYYY-MM-DD")),
		mcp.WithString("to", mcp.Required(), mcp.Description("End date YYYY-MM-DD")),
		mcp.WithString("name", mcp.Description("Optional exact workout name filter")),
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
