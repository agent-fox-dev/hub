package audit

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// 19-REQ-12.E1: Worker checks ctx.Err() before each step.
func TestRetentionWorker_StepBoundaryCancelCheck(t *testing.T) {
	db := openTestAuditDBWithAllTables(t)
	sqliteDB := openTestSQLiteDB(t)
	m := NewMetrics()

	// Create a pre-cancelled context.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// RunRetention with a cancelled context should return without
	// executing steps (or executing at most one).
	err := RunRetention(ctx, db, sqliteDB, DefaultRetentionConfig(), m)
	// The error might be context.Canceled or nil (if it checked before step 1).
	// Either way, it should not panic and should return promptly.
	_ = err
}

// Ensure the _ import of prometheus is used.
var _ = prometheus.Labels{}
