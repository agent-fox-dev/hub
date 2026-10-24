package audit

import (
	"context"
	"strings"
	"testing"

	"github.com/agent-fox-dev/hub/internal/testutil"
)

// The retention thresholds have defaults, come from the environment when
// set, and ignore unparseable or non-positive values.
func TestRetentionConfig_DefaultsAndEnvironment(t *testing.T) {
	want := RetentionConfig{MaxAgeDays: 90, MaxRuns: 50, TraceMaxAgeDays: 30, SessionMaxAgeDays: 90, PostmortemMaxAgeDays: 180, OrphanRetentionDays: 30, SessionMaxActiveAgeDays: 7}
	if got := DefaultRetentionConfig(); got != want {
		t.Errorf("defaults %+v; want %+v", got, want)
	}
	if got := LoadRetentionConfigFromEnv(); got != want {
		t.Errorf("without environment %+v; want the defaults", got)
	}
	t.Setenv("AF_AUDIT_MAX_AGE_DAYS", "10")
	t.Setenv("AF_AUDIT_MAX_RUNS", "5")
	t.Setenv("AF_TRACE_MAX_AGE_DAYS", "3")
	t.Setenv("AF_SESSION_MAX_AGE_DAYS", "0")
	t.Setenv("AF_POSTMORTEM_MAX_AGE_DAYS", "-1")
	t.Setenv("AF_AUDIT_ORPHAN_RETENTION_DAYS", "many")
	t.Setenv("AF_SESSION_MAX_ACTIVE_AGE_DAYS", "1")
	want.MaxAgeDays, want.MaxRuns, want.TraceMaxAgeDays, want.SessionMaxActiveAgeDays = 10, 5, 3, 1
	if got := LoadRetentionConfigFromEnv(); got != want {
		t.Errorf("from environment %+v; want %+v", got, want)
	}
}

// retentionEnv seeds every table with one aged row and one fresh row in the
// live workspace w and returns the env.
func retentionEnv(t *testing.T) *env {
	t.Helper()
	env := newEnv(t)
	old, fresh := daysAgo(400), now()
	for i, ts := range []string{old, fresh} {
		env.seedAgentEvent(t, agentEvent{id: uuidN(10 + i), ts: ts, run: "20260704_143022_00000" + string(rune('a'+i))})
		env.seedRow(t, "session_outcomes", uuidN(20+i), ts, map[string]any{"session_id": "s"})
		env.seedRow(t, "tool_calls", uuidN(30+i), ts, map[string]any{"tool_name": "t"})
		env.seedRow(t, "tool_errors", uuidN(40+i), ts, map[string]any{"tool_name": "t", "error_msg": "m"})
		env.seedTrace(t, uuidN(50+i), "n", "tool.use", ts, i, "")
		env.seedHubEvent(t, HubEventRow{ID: uuidN(60 + i), Workspace: "w", Timestamp: ts, IngestedAt: ts})
		testutil.Exec(t, env.duck, `INSERT INTO postmortems (run_id, workspace, run_status, started_at, completed_at, ingested_at) VALUES (?, 'w', 'stalled', ?, ?, ?)`,
			"20260704_143022_00000"+string(rune('a'+i)), ts, ts, ts)
		env.seedSession(t, Session{ID: uuidN(70 + i), WorkspaceSlug: "w", Status: "completed", StartedAt: ts})
		env.seedUsage(t, TokenUsage{ID: uuidN(80 + i), SessionID: uuidN(70 + i), WorkspaceSlug: "w", ReportedAt: ts})
	}
	return env
}

// Each step removes exactly the rows its threshold names: aged per-run
// agent records (events, outcomes, tool calls and errors together), aged hub
// events by ingestion, aged terminal sessions (never active ones), token
// usage of sessions that no longer exist, aged traces and aged postmortems.
func TestRetentionSteps_DeleteAgedRows(t *testing.T) {
	env := retentionEnv(t)
	env.seedSession(t, Session{ID: "active-old", WorkspaceSlug: "w", StartedAt: daysAgo(400)})
	ctx := context.Background()
	for _, c := range []struct {
		name   string
		step   func() (int64, error)
		tables []string
		want   int64
	}{
		{"agent records", func() (int64, error) { return RetentionStep1_DeleteAgedAgentRecords(ctx, env.duck, 90) }, []string{"agent_audit_events", "session_outcomes", "tool_calls", "tool_errors"}, 4},
		{"hub events", func() (int64, error) { return RetentionStep3_DeleteAgedHubEvents(ctx, env.duck, 90) }, []string{"hub_audit_events"}, 1},
		{"sessions", func() (int64, error) { return RetentionStep4_DeleteAgedSessions(ctx, env.duck, 90) }, []string{"agent_sessions"}, 1},
		{"orphaned usage", func() (int64, error) { return RetentionStep5_DeleteOrphanedTokenUsage(ctx, env.duck) }, []string{"token_usage"}, 1},
		{"traces", func() (int64, error) { return RetentionStep6_DeleteAgedTraces(ctx, env.duck, 30) }, []string{"agent_traces"}, 1},
		{"postmortems", func() (int64, error) { return RetentionStep7_DeleteAgedPostmortems(ctx, env.duck, 180) }, []string{"postmortems"}, 1},
	} {
		n, err := c.step()
		if err != nil || n != c.want {
			t.Errorf("%s: deleted %d, %v; want %d", c.name, n, err, c.want)
		}
		for _, table := range c.tables {
			if left := env.count(t, table, ""); left != 1+boolInt(table == "agent_sessions") {
				t.Errorf("%s: %d rows left in %s", c.name, left, table)
			}
		}
	}
	if env.count(t, "agent_sessions", "id = 'active-old'") != 1 {
		t.Error("an active session was deleted")
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// The run cap deletes the oldest runs' events when more distinct runs exist
// than allowed and nothing otherwise.
func TestRetentionStep_EnforcesMaxRuns(t *testing.T) {
	env := newEnv(t)
	for i := 0; i < 4; i++ {
		run := "20260704_143022_00000" + string(rune('a'+i))
		env.seedAgentEvent(t, agentEvent{id: uuidN(i), run: run, ts: at(i)})
		env.seedAgentEvent(t, agentEvent{id: uuidN(10 + i), run: run, ts: at(i + 10)})
	}
	ctx := context.Background()
	if n, err := RetentionStep2_EnforceMaxRuns(ctx, env.duck, 4); err != nil || n != 0 {
		t.Errorf("below the cap: %d, %v", n, err)
	}
	if n, err := RetentionStep2_EnforceMaxRuns(ctx, env.duck, 2); err != nil || n != 4 {
		t.Errorf("above the cap: %d, %v; want 4 rows of the two oldest runs", n, err)
	}
	if env.count(t, "agent_audit_events", "run_id LIKE '%a' OR run_id LIKE '%b'") != 0 || env.count(t, "agent_audit_events", "") != 4 {
		t.Error("the wrong runs were deleted")
	}
}

// Active sessions older than the active-age limit are closed with status
// timeout before the aged sessions are deleted.
func TestRetentionPreStep_ClosesStaleActiveSessions(t *testing.T) {
	env := newEnv(t)
	env.seedSession(t, Session{ID: "stale", WorkspaceSlug: "w", StartedAt: daysAgo(8)})
	env.seedSession(t, Session{ID: "fresh", WorkspaceSlug: "w", StartedAt: daysAgo(6)})
	env.seedSession(t, Session{ID: "done", WorkspaceSlug: "w", Status: "completed", StartedAt: daysAgo(8)})
	n, err := RetentionPreStep_ForceCloseOrphanedSessions(context.Background(), env.duck, 7)
	if err != nil || n != 1 {
		t.Fatalf("closed %d, %v; want 1", n, err)
	}
	s, _ := env.store.GetSession(context.Background(), "stale")
	if s.Status != "timeout" || s.EndedAt == nil {
		t.Errorf("stale session %+v; want timeout with ended_at", s)
	}
	if env.count(t, "agent_sessions", "status = 'active'") != 1 || env.count(t, "agent_sessions", "status = 'completed'") != 1 {
		t.Error("other sessions were changed")
	}
}

// Rows of workspaces that no longer exist in SQLite go once they are older
// than the grace period; rows without a workspace stay, and with no
// workspace at all every workspace row past the grace period goes.
func TestRetentionStep_DeletesOrphanedWorkspaceData(t *testing.T) {
	env := retentionEnv(t)
	old := daysAgo(400)
	env.seedAgentEvent(t, agentEvent{id: uuidN(1), ws: "gone", ts: old})
	env.seedAgentEvent(t, agentEvent{id: uuidN(2), ws: "gone", ts: now()})
	env.seedHubEvent(t, HubEventRow{ID: uuidN(3), Workspace: "gone", Timestamp: old, IngestedAt: old})
	env.seedHubEvent(t, HubEventRow{ID: uuidN(4), Workspace: "", Timestamp: old, IngestedAt: old})
	env.seedSession(t, Session{ID: "gone-s", WorkspaceSlug: "gone", StartedAt: old})
	testutil.Exec(t, env.duck, "UPDATE agent_sessions SET ingested_at = CAST(? AS TIMESTAMPTZ) WHERE id = 'gone-s'", old)
	ctx := context.Background()
	n, err := RetentionStep8_DeleteOrphanedWorkspaceData(ctx, env.duck, env.sqlite, 30)
	if err != nil || n != 3 {
		t.Fatalf("deleted %d, %v; want 3", n, err)
	}
	if env.count(t, "agent_audit_events", "workspace = 'gone'") != 1 || env.count(t, "hub_audit_events", "workspace = ''") != 1 || env.count(t, "agent_audit_events", "workspace = 'w'") != 2 {
		t.Error("the wrong rows were deleted")
	}
	testutil.Exec(t, env.sqlite, "DELETE FROM workspaces")
	n, err = RetentionStep8_DeleteOrphanedWorkspaceData(ctx, env.duck, env.sqlite, 30)
	if err != nil || n != 7 {
		t.Errorf("with no workspaces: deleted %d, %v; want the 7 aged rows of w", n, err)
	}
	if env.count(t, "hub_audit_events", "workspace = ''") != 1 || env.count(t, "agent_audit_events", "") != 2 {
		t.Error("fresh rows or workspace-less rows were deleted")
	}
	testutil.Exec(t, env.sqlite, "DROP TABLE workspaces")
	if _, err := RetentionStep8_DeleteOrphanedWorkspaceData(ctx, env.duck, env.sqlite, 30); err == nil {
		t.Error("a failed SQLite query was not reported")
	}
}

// The gauges are recalibrated from the tables: a row count per table and
// the active sessions per workspace, stale labels cleared; without metrics
// the step does nothing.
func TestRetentionStep_RecalibratesGauges(t *testing.T) {
	env := retentionEnv(t)
	env.seedSession(t, Session{ID: "a1", WorkspaceSlug: "w"})
	env.seedSession(t, Session{ID: "a2", WorkspaceSlug: "w"})
	env.metrics.AgentSessionsActive.WithLabelValues("stale").Set(5)
	ctx := context.Background()
	if err := RetentionStep9_RecalibrateGauges(ctx, env.duck, env.metrics); err != nil {
		t.Fatal(err)
	}
	if got := gauge(t, env.metrics.AuditTableRows, "agent_sessions"); got != 4 {
		t.Errorf("agent_sessions rows gauge %v; want 4", got)
	}
	if got := gauge(t, env.metrics.AgentSessionsActive, "w"); got != 2 {
		t.Errorf("active sessions of w %v; want 2", got)
	}
	if text := scrape(t, env.metrics); !strings.Contains(text, `afhub_agent_sessions_active{workspace="w"} 2`) || strings.Contains(text, `workspace="stale"`) {
		t.Errorf("scrape kept the stale label or lost w:\n%s", text)
	}
	if err := RetentionStep9_RecalibrateGauges(ctx, env.duck, nil); err != nil {
		t.Errorf("without metrics: %v", err)
	}
	testutil.Exec(t, env.duck, "DROP TABLE postmortems")
	if err := RetentionStep9_RecalibrateGauges(ctx, env.duck, env.metrics); err == nil {
		t.Error("a failed count was not reported")
	}
}

// A full run deletes the aged rows of every table and records its time; a
// failing step is counted on the errors counter, the other steps still run
// and the time is not recorded; a cancelled context stops before any step.
func TestRunRetention_RunsEveryStepAndRecordsOutcome(t *testing.T) {
	env := retentionEnv(t)
	env.seedSession(t, Session{ID: "stale", WorkspaceSlug: "w", StartedAt: daysAgo(400)})
	cfg := DefaultRetentionConfig()
	ctx := context.Background()
	if err := RunRetention(ctx, env.duck, env.sqlite, cfg, env.metrics); err != nil {
		t.Fatalf("RunRetention: %v", err)
	}
	for _, table := range auditTables {
		if n := env.count(t, table, ""); n != 1 {
			t.Errorf("%d rows left in %s; want the fresh one", n, table)
		}
	}
	if got := metricValue(t, env.metrics.RetentionLastRunTimestamp); got == 0 {
		t.Error("last run timestamp not set")
	}
	env.metrics.RetentionLastRunTimestamp.Set(0)
	testutil.Exec(t, env.duck, "DROP TABLE postmortems")
	env.seedAgentEvent(t, agentEvent{id: uuidN(99), ts: daysAgo(400)})
	if err := RunRetention(ctx, env.duck, env.sqlite, cfg, env.metrics); err == nil {
		t.Error("a failing step did not fail the run")
	}
	if env.count(t, "agent_audit_events", "") != 1 {
		t.Error("the other steps did not run after a failure")
	}
	if got := counter(t, env.metrics.RetentionErrorsTotal, "step_7_aged_postmortems"); got != 1 {
		t.Errorf("errors of step 7: %v; want 1", got)
	}
	if got := counter(t, env.metrics.RetentionErrorsTotal, "step_8_orphaned_workspace"); got != 1 {
		t.Errorf("errors of step 8 (its postmortems delete fails too): %v; want 1", got)
	}
	if got := counter(t, env.metrics.RetentionErrorsTotal, "step_9_recalibrate_gauges"); got != 1 {
		t.Errorf("errors of step 9: %v; want 1", got)
	}
	if got := metricValue(t, env.metrics.RetentionLastRunTimestamp); got != 0 {
		t.Error("last run timestamp set after a failure")
	}
	if err := RunRetention(ctx, env.duck, env.sqlite, cfg, nil); err == nil {
		t.Error("without metrics the failure is not reported")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	env.seedAgentEvent(t, agentEvent{id: uuidN(98), ts: daysAgo(400)})
	if err := RunRetention(cancelled, env.duck, env.sqlite, cfg, env.metrics); err != context.Canceled {
		t.Errorf("cancelled run: %v; want context.Canceled", err)
	}
	if env.count(t, "agent_audit_events", "") != 2 {
		t.Error("a cancelled run deleted rows")
	}
	for _, step := range []func() error{
		func() error { _, err := RetentionStep1_DeleteAgedAgentRecords(cancelled, env.duck, 1); return err },
		func() error { _, err := RetentionStep2_EnforceMaxRuns(cancelled, env.duck, 1); return err },
		func() error { _, err := RetentionStep3_DeleteAgedHubEvents(cancelled, env.duck, 1); return err },
		func() error { _, err := RetentionStep4_DeleteAgedSessions(cancelled, env.duck, 1); return err },
		func() error { _, err := RetentionStep5_DeleteOrphanedTokenUsage(cancelled, env.duck); return err },
		func() error { _, err := RetentionStep6_DeleteAgedTraces(cancelled, env.duck, 1); return err },
		func() error { _, err := RetentionStep7_DeleteAgedPostmortems(cancelled, env.duck, 1); return err },
		func() error {
			_, err := RetentionStep8_DeleteOrphanedWorkspaceData(cancelled, env.duck, env.sqlite, 1)
			return err
		},
		func() error { return RetentionStep9_RecalibrateGauges(cancelled, env.duck, env.metrics) },
		func() error {
			_, err := RetentionPreStep_ForceCloseOrphanedSessions(cancelled, env.duck, 1)
			return err
		},
	} {
		if err := step(); err != context.Canceled {
			t.Errorf("step with a cancelled context: %v; want context.Canceled", err)
		}
	}
}
