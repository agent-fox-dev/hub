package sandboxes

import (
	"errors"
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/agent-fox-dev/agentkit-go/sandbox/gateway"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd"
)

// isNotFound reports whether err says the box is gone: gatewayd's ErrNotHeld, or
// a gRPC NotFound status, wherever in the chain of wrapped errors it is. AgentKit
// has no not-found sentinel of its own, and a remote gateway can say it either
// way, so a box that is already gone counts as destroyed in both.
func isNotFound(err error) bool {
	return err != nil && (errors.Is(err, gatewayd.ErrNotHeld) || status.Code(err) == codes.NotFound)
}

// isRemoteRefusal reports whether err is the remote gateway refusing hub's token:
// the gateway client's ErrUnauthenticated, which is how it reports an
// Unauthenticated status, or an Unauthenticated or PermissionDenied status.
func isRemoteRefusal(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, gateway.ErrUnauthenticated) {
		return true
	}
	code := status.Code(err)
	return code == codes.Unauthenticated || code == codes.PermissionDenied
}

// isRemote reports whether the provider is a remote gateway.
func (s *Service) isRemote() bool { return s.remote != nil }

// remoteFailure returns the error a caller gets for a call to the provider that
// failed with err, whose scrubbed text is msg. A remote that refused hub's token
// is answered Unavailable and not with the remote's code, so that a caller never
// mistakes it for hub refusing the caller's own lease token: the fault is in
// hub's sandbox.gateway_token, and the one error record says so, without the
// token. Any other failure is the plain error of msg.
func (s *Service) remoteFailure(call string, err error, msg string) error {
	if !s.isRemote() || !isRemoteRefusal(err) {
		return errors.New(msg)
	}
	slog.Error("sandbox gateway: the remote gateway refused hub's token; check sandbox.gateway_token", "call", call, "error", msg)
	return &statusError{code: codes.Unavailable, msg: msg}
}
