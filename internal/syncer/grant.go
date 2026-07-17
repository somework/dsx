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

// The finalize_plan self-authorisation path.
//
// Gathered here because `dsx rm`, `dsx put` and push's delete path all reach
// them, and what they share is a protocol obligation, not a command.

// PlanToken mints a plan_token for exactly the writes or deletes described.
//
// Shared by `dsx rm` and push's delete path so the two cannot disagree about
// what a missing token means: a missing plan_token is a protocol fault, not a
// tool error -- the server answered without the one field the call exists to
// produce.
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

// planFor authorises writes to exactly these paths.
func planFor(ctx context.Context, c *mcp.Client, projectID string, paths []string) (string, error) {
	return PlanToken(ctx, c, map[string]any{"project_id": projectID, "writes": paths})
}

// CallWithGrant runs a write tool, self-authorising if the server demands a
// standing project grant.
//
// Without a grant the server answers HTTP 403; a path-scoped plan_token
// authorises exactly these paths instead, so the write proceeds without sending
// anyone to a browser. A project with no standing grant is the default, so this
// is the ordinary path rather than a rescue.
//
// It is shared because push had it and put did not: the same write that push
// completed left `dsx put` at exit 1 with no next step. Two copies would drift
// again.
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
