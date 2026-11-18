package mcpserver

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"health-receiver/internal/api"
	"health-receiver/internal/ctxdb"
	"health-receiver/internal/health"
)

func registerSleepComparisonTool(s *server.MCPServer) {
	s.AddTool(mcp.NewTool("get_sleep_duration_comparison",
		mcp.WithDescription("Compare the recorded main night's duration with the mean of comparable finalized nights in the preceding 30 local calendar dates. A null comparison means the feature is disabled. A preliminary result describes recorded duration. Duration differences alone do not establish sleep quality or causes."),
		mcp.WithString("date", mcp.Description("Wake date YYYY-MM-DD in the tenant's timezone (default: today)")),
		mcp.WithOutputSchema[api.SleepDurationComparisonResponse](),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		db := ctxdb.FromContext(ctx)
		if db == nil {
			return mcp.NewToolResultError("authenticated tenant required"), nil
		}
		date, err := sleepToolDate(req, db.Today)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if _, err := health.ParseSleepComparisonDate(date); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultJSON(api.SleepDurationComparisonResponse{Comparison: db.GetSleepDurationComparison(ctx, date)})
	})
}
