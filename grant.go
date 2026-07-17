package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// The finalize_plan self-authorisation path.
//
// These three sat in two different files, both above the layer they belong to:
// planToken among the CLI commands, planFor and callWithGrant among push's
// batching. Neither placement was chosen -- each function landed wherever its
// first caller happened to be, and their own comments said so. They are gathered
// here because `dsx rm`, `dsx put` and push's delete path all reach them, and
// what they share is a protocol obligation, not a command.

// planToken mints a plan_token for exactly the writes or deletes described.
//
// Shared by `dsx rm` and push's delete path so the two cannot disagree about
// what a missing token means: a missing plan_token is a protocol fault, not a
// tool error -- the server answered without the one field the call exists to
// produce.
func planToken(ctx context.Context, c *client, args map[string]any) (string, error) {
	text, err := c.callTool(ctx, "finalize_plan", args)
	if err != nil {
		return "", fmt.Errorf("finalize_plan: %w", err)
	}
	var plan struct {
		PlanToken string `json:"plan_token"`
	}
	if err := json.Unmarshal([]byte(text), &plan); err != nil || plan.PlanToken == "" {
		return "", &dsxError{Kind: kindProtocol,
			Msg: "finalize_plan returned no plan_token: " + truncate(text, 200)}
	}
	return plan.PlanToken, nil
}

// planFor authorises writes to exactly these paths.
func (c *client) planFor(ctx context.Context, projectID string, paths []string) (string, error) {
	return planToken(ctx, c, map[string]any{"project_id": projectID, "writes": paths})
}

// callWithGrant runs a write tool, self-authorising if the server demands a
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
func (c *client) callWithGrant(ctx context.Context, tool string, args map[string]any, projectID string, paths []string) (string, error) {
	text, err := c.callTool(ctx, tool, args)

	var ge *grantError
	if errors.As(err, &ge) {
		token, planErr := c.planFor(ctx, projectID, paths)
		if planErr != nil {
			return "", fmt.Errorf("%w; and could not self-authorise: %v", err, planErr)
		}
		args["plan_token"] = token
		return c.callTool(ctx, tool, args)
	}
	return text, err
}
