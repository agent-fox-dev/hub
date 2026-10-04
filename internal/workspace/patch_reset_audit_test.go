package workspace

import (
	"net/http"
	"testing"

	"github.com/agent-fox-dev/hub/internal/audit"

	_ "modernc.org/sqlite"
)

// ===========================================================================
// TS-23-64 (integration): A successful reset emits one hub.patch.reset event
// with the specified actor, resource and metadata for each action
// ===========================================================================

func TestResetAudit_HubPatchResetPerAction_TS2364(t *testing.T) {
	actions := []struct {
		name   string
		action ResetAction
		result ResetResult
	}{
		{
			name:   "none",
			action: ResetActionNone,
			result: ResetResult{
				Action:    ResetActionNone,
				LocalSHA:  "1111111111111111111111111111111111111111",
				OriginSHA: "2222222222222222222222222222222222222222",
			},
		},
		{
			name:   "created",
			action: ResetActionCreated,
			result: ResetResult{
				Action:           ResetActionCreated,
				OriginSHA:        "3333333333333333333333333333333333333333",
				RebuildTriggered: true,
				RebuildJobID:     "job-created",
			},
		},
		{
			name:   "fast_forwarded",
			action: ResetActionFastForwarded,
			result: ResetResult{
				Action:           ResetActionFastForwarded,
				LocalSHA:         "4444444444444444444444444444444444444444",
				OriginSHA:        "5555555555555555555555555555555555555555",
				RebuildTriggered: true,
				RebuildJobID:     "job-ff",
			},
		},
		{
			name:   "replaced",
			action: ResetActionReplaced,
			result: ResetResult{
				Action:           ResetActionReplaced,
				LocalSHA:         "6666666666666666666666666666666666666666",
				OriginSHA:        "7777777777777777777777777777777777777777",
				ReplacedSHA:      "6666666666666666666666666666666666666666",
				RebuildTriggered: true,
				RebuildJobID:     "job-replaced",
			},
		},
	}

	for _, tc := range actions {
		t.Run(tc.name, func(t *testing.T) {
			const slug = "audit-reset"
			const patchID = "p-audit"
			const branch = "feature/audit"

			env := newPatchTestEnv(t, slug, "main")
			_, err := env.db.Exec(`UPDATE workspaces SET clone_status = 'ready' WHERE slug = ?`, slug)
			if err != nil {
				t.Fatalf("update clone_status: %v", err)
			}
			seedPatchRaw(t, env.db, patchID, slug, branch, 1)

			saved := recoveryHook
			t.Cleanup(func() { recoveryHook = saved })

			hook := &stubRecoveryHookWithCalls{
				resetResult: tc.result,
			}
			RegisterRecoveryHook(hook)

			// Install a recording audit emitter.
			savedEmitter := defaultAuditEmitter
			t.Cleanup(func() { defaultAuditEmitter = savedEmitter })
			emitter := &stubAuditEmitter{}
			defaultAuditEmitter = emitter

			auth := userAuth("user-1")
			rec := env.doRequest(t, http.MethodPost, resetPath(slug, patchID), "", auth)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d; want 200; body: %s", rec.Code, rec.Body.String())
			}

			// Filter for hub.patch.reset events.
			var resetEvents []audit.HubEvent
			for _, ev := range emitter.events {
				if ev.EventType == audit.EventPatchReset {
					resetEvents = append(resetEvents, ev)
				}
			}

			if len(resetEvents) != 1 {
				t.Fatalf("hub.patch.reset events = %d; want 1", len(resetEvents))
			}

			ev := resetEvents[0]

			// resource_type is patch.
			if ev.ResourceType != "patch" {
				t.Errorf("resource_type = %q; want %q", ev.ResourceType, "patch")
			}

			// Workspace slug is set.
			if ev.Workspace != slug {
				t.Errorf("workspace = %q; want %q", ev.Workspace, slug)
			}

			// Actor is the caller.
			if ev.ActorID != "user-1" {
				t.Errorf("actor_id = %q; want %q", ev.ActorID, "user-1")
			}

			// Metadata checks.
			meta := ev.Metadata
			if meta["branch_name"] != branch {
				t.Errorf("meta.branch_name = %v; want %q", meta["branch_name"], branch)
			}
			if meta["action"] != string(tc.action) {
				t.Errorf("meta.action = %v; want %q", meta["action"], string(tc.action))
			}
			if meta["origin_sha"] != tc.result.OriginSHA {
				t.Errorf("meta.origin_sha = %v; want %q", meta["origin_sha"], tc.result.OriginSHA)
			}
		})
	}
}

// ===========================================================================
// TS-23-65 (property): hub.patch.reset metadata omits local_sha without a
// local branch and carries replaced_sha only for replaced
// ===========================================================================

func TestResetAudit_MetadataLocalAndReplacedSHA_TS2365(t *testing.T) {
	type outcome struct {
		name     string
		result   ResetResult
		hadLocal bool
	}

	outcomes := []outcome{
		{
			name: "created_no_local",
			result: ResetResult{
				Action:    ResetActionCreated,
				LocalSHA:  "", // no local branch
				OriginSHA: "aaaa",
			},
			hadLocal: false,
		},
		{
			name: "none_with_local",
			result: ResetResult{
				Action:    ResetActionNone,
				LocalSHA:  "bbbb",
				OriginSHA: "bbbb",
			},
			hadLocal: true,
		},
		{
			name: "fast_forwarded_with_local",
			result: ResetResult{
				Action:    ResetActionFastForwarded,
				LocalSHA:  "cccc",
				OriginSHA: "dddd",
			},
			hadLocal: true,
		},
		{
			name: "replaced_with_local",
			result: ResetResult{
				Action:      ResetActionReplaced,
				LocalSHA:    "eeee",
				OriginSHA:   "ffff",
				ReplacedSHA: "eeee",
			},
			hadLocal: true,
		},
	}

	for _, o := range outcomes {
		t.Run(o.name, func(t *testing.T) {
			const slug = "audit-meta"
			const patchID = "p-meta"
			const branch = "feature/meta"

			env := newPatchTestEnv(t, slug, "main")
			_, err := env.db.Exec(`UPDATE workspaces SET clone_status = 'ready' WHERE slug = ?`, slug)
			if err != nil {
				t.Fatalf("update clone_status: %v", err)
			}
			seedPatchRaw(t, env.db, patchID, slug, branch, 1)

			saved := recoveryHook
			t.Cleanup(func() { recoveryHook = saved })

			hook := &stubRecoveryHookWithCalls{
				resetResult: o.result,
			}
			RegisterRecoveryHook(hook)

			savedEmitter := defaultAuditEmitter
			t.Cleanup(func() { defaultAuditEmitter = savedEmitter })
			emitter := &stubAuditEmitter{}
			defaultAuditEmitter = emitter

			auth := userAuth("user-1")
			rec := env.doRequest(t, http.MethodPost, resetPath(slug, patchID), "", auth)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d; want 200; body: %s", rec.Code, rec.Body.String())
			}

			// Find the hub.patch.reset event.
			var resetEvent *audit.HubEvent
			for i := range emitter.events {
				if emitter.events[i].EventType == audit.EventPatchReset {
					resetEvent = &emitter.events[i]
					break
				}
			}
			if resetEvent == nil {
				t.Fatal("no hub.patch.reset event emitted")
			}

			meta := resetEvent.Metadata

			// Verify action is one of the four allowed values.
			action, _ := meta["action"].(string)
			allowed := map[string]bool{"none": true, "created": true, "fast_forwarded": true, "replaced": true}
			if !allowed[action] {
				t.Errorf("action = %q; want one of none, created, fast_forwarded, replaced", action)
			}

			// local_sha key is absent exactly when there was no local branch.
			_, hasLocalSHA := meta["local_sha"]
			if hasLocalSHA != o.hadLocal {
				t.Errorf("local_sha present = %v; want %v (hadLocal = %v)", hasLocalSHA, o.hadLocal, o.hadLocal)
			}

			// replaced_sha key is present exactly when action is replaced.
			_, hasReplacedSHA := meta["replaced_sha"]
			isReplaced := o.result.Action == ResetActionReplaced
			if hasReplacedSHA != isReplaced {
				t.Errorf("replaced_sha present = %v; want %v (action = %q)", hasReplacedSHA, isReplaced, o.result.Action)
			}

			// When replaced_sha is present, it equals the pre-reset local tip.
			if isReplaced {
				if meta["replaced_sha"] != o.result.ReplacedSHA {
					t.Errorf("replaced_sha = %v; want %q", meta["replaced_sha"], o.result.ReplacedSHA)
				}
			}
		})
	}
}

// ===========================================================================
// TS-23-66 (integration): A replaced reset also emits hub.patch.replace
// with trigger reset_to_origin
// ===========================================================================

func TestResetAudit_ReplacedEmitsHubPatchReplace_TS2366(t *testing.T) {
	const slug = "audit-replace"
	const patchID = "p-replace"
	const branch = "feature/replace"
	const localBefore = "1111111111111111111111111111111111111111"
	const originSHA = "2222222222222222222222222222222222222222"

	env := newPatchTestEnv(t, slug, "main")
	_, err := env.db.Exec(`UPDATE workspaces SET clone_status = 'ready' WHERE slug = ?`, slug)
	if err != nil {
		t.Fatalf("update clone_status: %v", err)
	}
	seedPatchRaw(t, env.db, patchID, slug, branch, 1)

	saved := recoveryHook
	t.Cleanup(func() { recoveryHook = saved })

	hook := &stubRecoveryHookWithCalls{
		readSHA:   localBefore,
		readFound: true,
		resetResult: ResetResult{
			Action:      ResetActionReplaced,
			LocalSHA:    localBefore,
			OriginSHA:   originSHA,
			ReplacedSHA: localBefore,
		},
	}
	RegisterRecoveryHook(hook)

	savedEmitter := defaultAuditEmitter
	t.Cleanup(func() { defaultAuditEmitter = savedEmitter })
	emitter := &stubAuditEmitter{}
	defaultAuditEmitter = emitter

	auth := userAuth("user-1")
	rec := env.doRequest(t, http.MethodPost, resetPath(slug, patchID), "", auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200; body: %s", rec.Code, rec.Body.String())
	}

	// Find hub.patch.replace events.
	var replaceEvents []audit.HubEvent
	for _, ev := range emitter.events {
		if ev.EventType == audit.EventPatchReplace {
			replaceEvents = append(replaceEvents, ev)
		}
	}

	if len(replaceEvents) != 1 {
		t.Fatalf("hub.patch.replace events = %d; want 1", len(replaceEvents))
	}

	ev := replaceEvents[0]
	meta := ev.Metadata

	if meta["branch_name"] != branch {
		t.Errorf("meta.branch_name = %v; want %q", meta["branch_name"], branch)
	}
	if meta["replaced_sha"] != localBefore {
		t.Errorf("meta.replaced_sha = %v; want %q", meta["replaced_sha"], localBefore)
	}
	if meta["origin_sha"] != originSHA {
		t.Errorf("meta.origin_sha = %v; want %q", meta["origin_sha"], originSHA)
	}
	if meta["trigger"] != "reset_to_origin" {
		t.Errorf("meta.trigger = %v; want %q", meta["trigger"], "reset_to_origin")
	}

	// Verify that non-replaced actions do NOT emit hub.patch.replace.
	for _, action := range []ResetAction{ResetActionNone, ResetActionCreated, ResetActionFastForwarded} {
		t.Run("no_replace_for_"+string(action), func(t *testing.T) {
			emitter2 := &stubAuditEmitter{}
			defaultAuditEmitter = emitter2

			hook.resetResult = ResetResult{
				Action:    action,
				LocalSHA:  "aaaa",
				OriginSHA: "bbbb",
			}

			rec := env.doRequest(t, http.MethodPost, resetPath(slug, patchID), "", auth)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d; want 200; body: %s", rec.Code, rec.Body.String())
			}

			for _, ev := range emitter2.events {
				if ev.EventType == audit.EventPatchReplace {
					t.Errorf("hub.patch.replace emitted for action %q; want none", action)
				}
			}
		})
	}
}

// ===========================================================================
// TS-23-68 (integration): A failed reset emits no audit event
// ===========================================================================

func TestResetAudit_FailedResetNoEvents_TS2368(t *testing.T) {
	const slug = "audit-fail"
	const patchID = "p-fail"

	env := newPatchTestEnv(t, slug, "main")
	_, err := env.db.Exec(`UPDATE workspaces SET clone_status = 'ready' WHERE slug = ?`, slug)
	if err != nil {
		t.Fatalf("update clone_status: %v", err)
	}
	seedPatchRaw(t, env.db, patchID, slug, "feature/fail", 1)

	saved := recoveryHook
	t.Cleanup(func() { recoveryHook = saved })

	savedEmitter := defaultAuditEmitter
	t.Cleanup(func() { defaultAuditEmitter = savedEmitter })

	auth := userAuth("user-1")

	failures := []struct {
		name string
		err  *ResetError
	}{
		{
			name: "busy",
			err:  &ResetError{Kind: ResetErrBusy, Message: "workspace busy"},
		},
		{
			name: "credential_failure",
			err:  &ResetError{Kind: ResetErrCredentialFailed, Message: "cred error"},
		},
		{
			name: "fetch_failure",
			err:  &ResetError{Kind: ResetErrFetchFailed, Message: "fetch error"},
		},
		{
			name: "missing_on_origin",
			err:  &ResetError{Kind: ResetErrMissingOnOrigin, Message: "not found"},
		},
		{
			name: "ref_changed",
			err:  &ResetError{Kind: ResetErrRefChanged, Message: "ref changed"},
		},
		{
			name: "other",
			err:  &ResetError{Kind: ResetErrOther, Message: "something broke"},
		},
	}

	for _, tc := range failures {
		t.Run(tc.name, func(t *testing.T) {
			emitter := &stubAuditEmitter{}
			defaultAuditEmitter = emitter

			hook := &stubRecoveryHookWithCalls{
				resetErr: tc.err,
			}
			RegisterRecoveryHook(hook)

			rec := env.doRequest(t, http.MethodPost, resetPath(slug, patchID), "", auth)

			// Should return an error status.
			if rec.Code == http.StatusOK {
				t.Fatalf("expected error status, got 200")
			}

			// Zero hub.patch.reset events.
			for _, ev := range emitter.events {
				if ev.EventType == audit.EventPatchReset {
					t.Errorf("hub.patch.reset emitted for failure %q; want none", tc.name)
				}
			}

			// Zero hub.patch.replace events.
			for _, ev := range emitter.events {
				if ev.EventType == audit.EventPatchReplace {
					t.Errorf("hub.patch.replace emitted for failure %q; want none", tc.name)
				}
			}
		})
	}
}


