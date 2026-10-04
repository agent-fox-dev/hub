package workspace

import (
	"context"
	"testing"

	"github.com/txsvc/apikit"
)

// stubRecoveryHook is a test double for RecoveryHook.
type stubRecoveryHook struct {
	readSHA   string
	readFound bool
	readErr   error

	removeErr error

	resetResult ResetResult
	resetErr    error
}

func (s *stubRecoveryHook) ReadReplacedSHA(_ context.Context, _, _ string) (string, bool, error) {
	return s.readSHA, s.readFound, s.readErr
}

func (s *stubRecoveryHook) RemoveBackup(_ context.Context, _, _ string) error {
	return s.removeErr
}

func (s *stubRecoveryHook) RunReset(_ context.Context, _ string, _ ResetPatchInfo, _ *apikit.AuthInfo) (ResetResult, error) {
	return s.resetResult, s.resetErr
}

// TS-23-59: The workspace package defines the recovery hook with a nil default,
// a registration function and three operations.
func TestRecoveryHook_NilDefault_RegisterAndOperations_TS2359(t *testing.T) {
	// Save and restore the global hook.
	saved := recoveryHook
	t.Cleanup(func() { recoveryHook = saved })

	// 1. Default is nil.
	recoveryHook = nil
	if got := getRecoveryHook(); got != nil {
		t.Fatalf("default recovery hook should be nil, got %v", got)
	}

	// 2. After registration, handlers see the hook.
	stub := &stubRecoveryHook{
		readSHA:   "abc123",
		readFound: true,
	}
	RegisterRecoveryHook(stub)
	if got := getRecoveryHook(); got == nil {
		t.Fatal("after RegisterRecoveryHook, getRecoveryHook() should not be nil")
	}
	if got := getRecoveryHook(); got != stub {
		t.Fatal("getRecoveryHook() should return the registered stub")
	}

	// 3. The type carries ReadReplacedSHA, RemoveBackup and RunReset.
	ctx := context.Background()
	sha, found, err := stub.ReadReplacedSHA(ctx, "slug", "branch")
	if sha != "abc123" || !found || err != nil {
		t.Errorf("ReadReplacedSHA = (%q, %v, %v); want (abc123, true, nil)", sha, found, err)
	}

	if err := stub.RemoveBackup(ctx, "slug", "branch"); err != nil {
		t.Errorf("RemoveBackup = %v; want nil", err)
	}

	// RunReset takes slug, ResetPatchInfo and *apikit.AuthInfo.
	result, err := stub.RunReset(ctx, "slug", ResetPatchInfo{
		ID:                "patch-1",
		BranchName:        "feature/x",
		Status:            "active",
		IntegrationBranch: "main",
	}, &apikit.AuthInfo{UserID: "user-1"})
	if err != nil {
		t.Errorf("RunReset = %v; want nil", err)
	}
	_ = result
}
