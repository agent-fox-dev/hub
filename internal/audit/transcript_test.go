package audit

import (
	"net/http"
	"testing"

	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/testutil"
)

const transcriptPath = runPath + "/transcript"

type transcript struct {
	RunID    string          `json:"run_id"`
	NodeID   string          `json:"node_id"`
	Messages []transcriptMsg `json:"messages"`
}

// GET .../transcript?node_id= rebuilds the conversation of a node from its
// traces in time and sequence order, mapping each trace type to a role and
// skipping traces it does not know, confined to the workspace and node.
func TestTranscript_RebuildsConversation(t *testing.T) {
	env := newEnv(t)
	env.seedTrace(t, uuidN(1), "n1", "session.init", at(0), 0, `{"content":"system prompt"}`)
	env.seedTrace(t, uuidN(2), "n1", "assistant.message", at(1), 2, `{"content":"second"}`)
	env.seedTrace(t, uuidN(3), "n1", "tool.use", at(1), 1, `{"content":"ls","tool_name":"bash"}`)
	env.seedTrace(t, uuidN(4), "n1", "tool.error", at(2), 3, `{"content":"boom","tool_name":"bash"}`)
	env.seedTrace(t, uuidN(5), "n1", "session.result", at(3), 4, `{"content":"done"}`)
	env.seedTrace(t, uuidN(6), "n1", "assistant.message", at(4), 5, `not json`)
	env.seedTrace(t, uuidN(7), "n2", "assistant.message", at(0), 0, `{"content":"other node"}`)
	env.seedRow(t, "agent_traces", uuidN(8), at(0), map[string]any{"workspace": "w-own", "node_id": "n1", "event_type": "assistant.message", "data": `{"content":"other ws"}`})
	tr := decode[transcript](t, env.must(t, http.StatusOK, http.MethodGet, transcriptPath+"?node_id=n1", "", member))
	if tr.RunID != runID || tr.NodeID != "n1" || len(tr.Messages) != 5 {
		t.Fatalf("transcript %+v", tr)
	}
	want := []struct{ role, content string }{{"system", "system prompt"}, {"tool_use", "ls"}, {"assistant", "second"}, {"tool_error", "boom"}, {"assistant", ""}}
	for i, w := range want {
		if m := tr.Messages[i]; m.Role != w.role || m.Content != w.content || m.Timestamp == "" {
			t.Errorf("message %d = %+v; want %v", i, m, w)
		}
	}
	if tr.Messages[1].ToolName == nil || *tr.Messages[1].ToolName != "bash" || tr.Messages[0].ToolName != nil {
		t.Errorf("tool names: %v %v", tr.Messages[1].ToolName, tr.Messages[0].ToolName)
	}
	if empty := decode[transcript](t, env.must(t, http.StatusOK, http.MethodGet, transcriptPath+"?node_id=n9", "", owner)); empty.Messages == nil || len(empty.Messages) != 0 {
		t.Errorf("unknown node: %+v; want an empty list", empty.Messages)
	}
}

func TestTranscript_Refusals(t *testing.T) {
	env := newEnv(t)
	for _, c := range []struct {
		name, path string
		auth       *apikit.AuthInfo
		status     int
		msg        string
	}{
		{"unauthenticated", transcriptPath + "?node_id=n1", nil, 401, "authentication required"},
		{"missing scope", transcriptPath + "?node_id=n1", testutil.PAT("u1", "sessions:read"), 403, "insufficient scope: audit:read required"},
		{"node required", transcriptPath, owner, 400, "node_id is required"},
		{"stranger", transcriptPath + "?node_id=n1", stranger, 404, "workspace not found"},
		{"bound elsewhere", "/api/v1/workspaces/w-arch/runs/" + runID + "/transcript?node_id=n1", bound, 404, "workspace not found"},
		{"admin anywhere", "/api/v1/workspaces/w-x/runs/" + runID + "/transcript?node_id=n1", admin, 200, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			expect(t, env.do(t, http.MethodGet, c.path, "", c.auth), c.status, c.msg)
		})
	}
	testutil.Exec(t, env.duck, "DROP TABLE agent_traces")
	expect(t, env.do(t, http.MethodGet, transcriptPath+"?node_id=n1", "", owner), 500, "internal server error")
}
