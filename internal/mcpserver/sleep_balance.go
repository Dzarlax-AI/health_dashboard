package mcpserver

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"health-receiver/internal/api"
	"health-receiver/internal/ctxdb"
	"health-receiver/internal/health"
)

type sleepBalanceToolOutput struct {
	WakeDate string                            `json:"wake_date"`
	Balance  *api.SleepDurationBalanceResponse `json:"balance"`
}

type sleepBalanceReader interface {
	Today() string
	GetSleepDurationBalance(context.Context, string) (*health.SleepDurationBalance, error)
}

func registerSleepBalanceTool(s *server.MCPServer) {
	s.AddTool(mcp.NewTool("get_sleep_balance",
		mcp.WithDescription("Read the server's saved 14-period sleep-duration balance. The balance compares sleep with the manual goal effective in each tenant-local noon-to-noon period. It is an accounting summary, not a physiological debt estimate or recommendation. Missing snapshots return balance=null; this tool never recalculates or writes."),
		mcp.WithString("date", mcp.Description("Wake date YYYY-MM-DD in the tenant's timezone (default: today)")),
		mcp.WithOutputSchema[sleepBalanceToolOutput](),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return handleSleepBalance(ctx, req, ctxdb.FromContext(ctx))
	})
}

func handleSleepBalance(ctx context.Context, req mcp.CallToolRequest, reader sleepBalanceReader) (*mcp.CallToolResult, error) {
	date, err := sleepToolDate(req, reader.Today)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	balance, err := reader.GetSleepDurationBalance(ctx, date)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	var output *api.SleepDurationBalanceResponse
	if balance != nil {
		response := api.NewSleepDurationBalanceResponse(*balance)
		output = &response
	}
	return mcp.NewToolResultJSON(sleepBalanceToolOutput{WakeDate: date, Balance: output})
}

func validateSleepBalanceDate(value string) error {
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil || parsed.Format("2006-01-02") != value || strings.TrimSpace(value) != value {
		return fmt.Errorf("date must be a valid YYYY-MM-DD date")
	}
	return nil
}

func sleepToolDate(req mcp.CallToolRequest, today func() string) (string, error) {
	if raw := req.GetRawArguments(); raw != nil {
		if _, ok := raw.(map[string]any); !ok {
			return "", fmt.Errorf("arguments must be an object containing only date")
		}
	}
	args := req.GetArguments()
	for key := range args {
		if key != "date" {
			return "", fmt.Errorf("unsupported argument %q", key)
		}
	}
	var date string
	if raw, exists := args["date"]; exists {
		value, ok := raw.(string)
		if !ok {
			return "", fmt.Errorf("date must be a string in YYYY-MM-DD format")
		}
		date = value
	} else {
		date = today()
	}
	if err := validateSleepBalanceDate(date); err != nil {
		return "", err
	}
	return date, nil
}
