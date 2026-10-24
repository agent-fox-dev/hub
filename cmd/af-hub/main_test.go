package main

import (
	"errors"
	"testing"
)

// The sandbox shutdown runs after the server's Start returns, whether it
// returns nil or an error, and what Start returned is returned.
func TestRunServer_ShutdownFollowsStart(t *testing.T) {
	for name, startErr := range map[string]error{"Start returned nil": nil, "Start failed": errors.New("bind: address already in use")} {
		t.Run(name, func(t *testing.T) {
			var calls []string
			err := runServer(
				func() error { calls = append(calls, "start"); return startErr },
				func() { calls = append(calls, "shutdown") },
			)
			if err != startErr {
				t.Errorf("runServer returned %v; want %v", err, startErr)
			}
			if len(calls) != 2 || calls[0] != "start" || calls[1] != "shutdown" {
				t.Errorf("calls = %v; want start, then shutdown", calls)
			}
		})
	}
}
