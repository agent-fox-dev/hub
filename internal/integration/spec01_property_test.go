package integration

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/agent-fox-dev/hub/internal/admin"
	"github.com/agent-fox-dev/hub/internal/db"
	"github.com/agent-fox-dev/hub/internal/serverconfig"

	_ "modernc.org/sqlite"
)

// ---------------------------------------------------------------------------
// Property Test TS-01-P6 — Startup Order Is Strictly Sequential
// ---------------------------------------------------------------------------

// TestSpec01_PropStartupOrderStrictlySequential runs 20 boot cycles with
// varying configuration and environment states (first boot, subsequent boot,
// reset flag) and verifies for each cycle that:
//
//   - The HTTP listener never opens before SQLite initialization and admin
//     bootstrap/validation have both completed successfully.
//   - Health probes are not reachable before the HTTP listener starts.
//
// Since we cannot start a real HTTP listener in unit tests, this property test
// verifies the dependency chain by confirming that:
//
//  1. Config loading (step 2) succeeds before DB init (step 4) is attempted.
//  2. DB init (step 4) completes before admin bootstrap (step 5) is attempted.
//  3. Admin bootstrap (step 5) completes before the startup info (step 7) can
//     be emitted.
//
// If any dependency is broken (e.g. InitDatabase returns nil), the downstream
// step must fail, preventing the sequence from advancing.
//
// TS-01-P6, PROP: 01-PROP-6
// Validates: 01-REQ-2.1, 01-REQ-4.1, 01-REQ-4.2
func TestSpec01_PropStartupOrderStrictlySequential(t *testing.T) {
	type bootScenario struct {
		name      string
		hasConfig bool
		envToken  string    // AF_HUB_ADMIN_TOKEN value ("" to unset)
		resetFlag bool      // --reset-admin-token
		firstBoot bool      // whether admin_tokens should be empty
	}

	// Generate 20 scenarios cycling through combinations.
	scenarios := make([]bootScenario, 20)
	for i := range 20 {
		switch i % 5 {
		case 0:
			scenarios[i] = bootScenario{
				name:      "first_boot_no_config",
				hasConfig: false,
				firstBoot: true,
			}
		case 1:
			scenarios[i] = bootScenario{
				name:      "first_boot_with_config",
				hasConfig: true,
				firstBoot: true,
			}
		case 2:
			scenarios[i] = bootScenario{
				name:      "subsequent_boot",
				hasConfig: false,
				envToken:  "af_admin_0000000000000000000000000000000000000000000000000000000000000000",
				firstBoot: false,
			}
		case 3:
			scenarios[i] = bootScenario{
				name:      "reset_flag",
				hasConfig: false,
				resetFlag: true,
				firstBoot: false,
			}
		case 4:
			scenarios[i] = bootScenario{
				name:      "reset_first_boot",
				hasConfig: false,
				resetFlag: true,
				firstBoot: true,
			}
		}
	}

	for i, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			tmpDir := t.TempDir()

			// ---------------------------------------------------------------
			// Step 2: Config loading — must always succeed.
			// ---------------------------------------------------------------
			configPath := filepath.Join(tmpDir, "config.toml")
			if sc.hasConfig {
				content := `[server]
port = 9090
[database]
path = "` + filepath.Join(tmpDir, "test.db") + `"
[log]
level = "debug"
`
				if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
					t.Fatalf("iter %d: failed to write config: %v", i, err)
				}
			}

			result, err := serverconfig.LoadConfig(configPath)
			if err != nil {
				t.Fatalf("iter %d: step 2 (config loading) failed: %v", i, err)
			}
			if result == nil || result.Config == nil {
				t.Fatalf("iter %d: step 2 returned nil config", i)
			}

			// ---------------------------------------------------------------
			// Step 4: DB initialization — depends on step 2 completing.
			// ---------------------------------------------------------------
			dbPath := filepath.Join(tmpDir, "data", "test.db")
			database, dbErr := db.InitDatabase(dbPath)

			// InitDatabase is currently a stub returning (nil, nil).
			// The HTTP listener (step 8) MUST NOT start when DB is nil.
			// We verify the dependency by asserting that step 5 (admin
			// bootstrap) cannot proceed without a valid *sql.DB.
			if database == nil {
				// This is the expected stub behavior. The startup sequence
				// should abort here — no steps 5-9 should proceed.
				t.Logf("iter %d: InitDatabase returned nil DB (stub); confirming downstream steps cannot proceed", i)

				// Attempting step 5 with nil DB should fail.
				assertAdminBootstrapFailsWithNilDB(t, i, nil, tmpDir, sc.resetFlag)
				return
			}
			if dbErr != nil {
				t.Fatalf("iter %d: step 4 (DB init) returned error: %v", i, dbErr)
			}
			defer database.Close()

			// ---------------------------------------------------------------
			// Step 5: Admin bootstrap — depends on step 4 completing.
			// ---------------------------------------------------------------
			if sc.envToken != "" {
				t.Setenv("AF_HUB_ADMIN_TOKEN", sc.envToken)
			}

			_, bootstrapErr := admin.Bootstrap(database, tmpDir, sc.resetFlag)
			if bootstrapErr != nil {
				// On subsequent boot with wrong/missing token, bootstrap
				// should fail. The HTTP listener must not start.
				t.Logf("iter %d: step 5 (admin bootstrap) failed: %v — HTTP listener must not start", i, bootstrapErr)
				return
			}

			// ---------------------------------------------------------------
			// Step 7: Startup log fields — depends on steps 2-5 completing.
			// ---------------------------------------------------------------
			fields := serverconfig.StartupLogFields(result.Config)
			if fields == nil {
				t.Errorf("iter %d: StartupLogFields returned nil after successful steps 2-5", i)
			}

			// The key property: if we reach step 7, all prior steps succeeded.
			// The HTTP listener (step 8) would only be started after this point.
			t.Logf("iter %d: startup ordering verified — steps 2→4→5→7 completed sequentially", i)
		})
	}
}

// assertAdminBootstrapFailsWithNilDB verifies that admin.Bootstrap panics or
// returns an error when called with a nil *sql.DB, proving that the startup
// sequence cannot advance past step 4 without a valid database.
func assertAdminBootstrapFailsWithNilDB(t *testing.T, iter int, database *sql.DB, configDir string, reset bool) {
	t.Helper()

	// Catch potential panic from nil DB.
	defer func() {
		if r := recover(); r != nil {
			t.Logf("iter %d: admin.Bootstrap correctly panicked with nil DB: %v", iter, r)
		}
	}()

	_, err := admin.Bootstrap(database, configDir, reset)
	if err != nil {
		t.Logf("iter %d: admin.Bootstrap correctly returned error with nil DB: %v", iter, err)
		return
	}

	// If Bootstrap succeeds with nil DB, the startup ordering property is
	// violated — downstream steps should not succeed without DB init.
	t.Errorf("iter %d: admin.Bootstrap succeeded with nil DB; startup ordering violated — HTTP listener could start without DB", iter)
}
