package mcp

import (
	"encoding/json"
	"testing"
)

func TestNormalizeSSE(t *testing.T) {
	t.Run("plain json passes through", func(t *testing.T) {
		in := `{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`
		if got := string(normalizeSSE([]byte(in), "application/json")); got != in {
			t.Errorf("got %q, want it untouched", got)
		}
	})

	// A notification ahead of the response is the case that concatenation
	// turns into invalid JSON.
	t.Run("picks the response out of a stream carrying notifications", func(t *testing.T) {
		in := "event: message\n" +
			`data: {"jsonrpc":"2.0","method":"notifications/progress","params":{"n":1}}` + "\n\n" +
			"event: message\n" +
			`data: {"jsonrpc":"2.0","id":1,"result":{"ok":true}}` + "\n\n"

		var out rpcResponse
		if err := json.Unmarshal(normalizeSSE([]byte(in), "text/event-stream"), &out); err != nil {
			t.Fatalf("result did not survive the stream: %v", err)
		}
		if string(out.Result) != `{"ok":true}` {
			t.Errorf("result = %s, want {\"ok\":true}", out.Result)
		}
	})

	t.Run("finds an error frame", func(t *testing.T) {
		in := "event: message\n" + `data: {"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"bad"}}` + "\n\n"

		var out rpcResponse
		if err := json.Unmarshal(normalizeSSE([]byte(in), "text/event-stream"), &out); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if out.Error == nil || out.Error.Code != -32602 {
			t.Errorf("error = %+v, want code -32602", out.Error)
		}
	})

	t.Run("joins multi-line data within one event", func(t *testing.T) {
		in := "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\ndata: \"result\":{\"ok\":true}}\n\n"

		var out rpcResponse
		if err := json.Unmarshal(normalizeSSE([]byte(in), "text/event-stream"), &out); err != nil {
			t.Fatalf("multi-line data frame did not reassemble: %v", err)
		}
		if string(out.Result) != `{"ok":true}` {
			t.Errorf("result = %s", out.Result)
		}
	})

	t.Run("tolerates CRLF", func(t *testing.T) {
		in := "event: message\r\n" + `data: {"jsonrpc":"2.0","id":1,"result":{"ok":true}}` + "\r\n\r\n"
		var out rpcResponse
		if err := json.Unmarshal(normalizeSSE([]byte(in), "text/event-stream"), &out); err != nil {
			t.Fatalf("CRLF stream failed: %v", err)
		}
	})
}

// A retried delete_files or write_files re-executes a mutation the server may
// already have applied.
func TestOnlyReadOnlyToolsAreRetried(t *testing.T) {
	mutating := []string{
		"write_files", "delete_files", "copy_files", "create_project",
		"create_support_js", "finalize_plan", "put_conversation",
		"add_member", "remove_member", "update_member_role",
		"update_sharing", "render_preview",
	}
	for _, name := range mutating {
		if readOnlyTools[name] {
			t.Errorf("%s is marked read-only; a transport fault would re-execute it", name)
		}
	}
	for _, name := range []string{"list_files", "read_file", "list_projects", "get_project"} {
		if !readOnlyTools[name] {
			t.Errorf("%s should be retryable", name)
		}
	}
}
