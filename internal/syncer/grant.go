package syncer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/somework/dsx/internal/dsxerr"
	"github.com/somework/dsx/internal/fmtutil"
	"github.com/somework/dsx/internal/mcp"
)

func PlanToken(ctx context.Context, c *mcp.Client, args map[string]any) (string, error) {
	text, err := c.CallTool(ctx, "finalize_plan", args)
	if err != nil {
		return "", fmt.Errorf("finalize_plan: %w", err)
	}
	var plan struct {
		PlanToken string `json:"plan_token"`
	}
	if err := json.Unmarshal([]byte(text), &plan); err != nil || plan.PlanToken == "" {
		return "", &dsxerr.Error{Kind: dsxerr.KindProtocol,
			Msg: "finalize_plan returned no plan_token: " + fmtutil.Truncate(text, 200)}
	}
	return plan.PlanToken, nil
}

func planFor(ctx context.Context, c *mcp.Client, projectID string, paths []string) (string, error) {
	return PlanToken(ctx, c, map[string]any{"project_id": projectID, "writes": paths})
}

func CallWithGrant(ctx context.Context, c *mcp.Client, tool string, args map[string]any, projectID string, paths []string) (string, error) {
	text, err := c.CallTool(ctx, tool, args)

	var ge *mcp.GrantError
	if errors.As(err, &ge) {
		token, planErr := planFor(ctx, c, projectID, paths)
		if planErr != nil {
			return "", fmt.Errorf("%w; and could not self-authorise: %v", err, planErr)
		}
		args["plan_token"] = token
		return c.CallTool(ctx, tool, args)
	}
	return text, err
}
