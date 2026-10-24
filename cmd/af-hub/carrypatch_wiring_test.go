package main

import (
	"os"
	"strings"
	"testing"
)

// The stale rebuild worktree cleanup must run before the queue starts, so no
// rebuild can be using the directories it removes (01-REQ-8); the rebuild job
// and its audit emitter must be registered. These are wiring facts of main.go
// that no package test can see.
func TestMainWiresCarryPatch(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	cleanup := strings.Index(s, "carrypatch.CleanupStaleRebuildWorktrees(")
	start := strings.Index(s, "mergeQueue.Start()")
	if cleanup < 0 || start < 0 || cleanup > start {
		t.Errorf("main.go must call CleanupStaleRebuildWorktrees before mergeQueue.Start() (cleanup at %d, start at %d)", cleanup, start)
	}
	for _, want := range []string{
		"carrypatch.RegisterRebuildJob(mergeQueue, rebuildHandler)",
		"rebuildHandler.Audit = cphost.Emitter(auditEmitter)",
		"cphost.Bind()",
		"carrypatch.RegisterRoutes(server.APIGroup(), carrypatch.APIConfig{",
		"workspace.RegisterSyncExtension(carrypatch.NewCarryPatchSyncHook(",
		"gitserver.RegisterPreReceiveHook(cphost.PreReceive(",
		"gitserver.RegisterPostPushHook(cphost.PostPush(",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("main.go does not contain %q", want)
		}
	}
}
