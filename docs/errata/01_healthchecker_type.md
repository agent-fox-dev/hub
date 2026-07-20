# Erratum: HealthChecker is a function type, not an interface

**Spec:** 01_workspaces
**Requirement:** 01-REQ-2.E1
**Task:** 11.2

## Divergence

The spec glossary defines `HealthChecker` as:

> An apikit interface implemented by hub using a database ping to report server health.

Task 11.2 instructs creating a struct that implements an interface:

> Create internal/health/checker.go implementing apikit.HealthChecker interface
> with a DB ping. dbHealthChecker.Check(ctx) error: execute a trivial SQL
> (SELECT 1) against *apikit.DB; return error if ping fails.

## Actual API

In the apikit library, `HealthChecker` is defined as a **function type**:

```go
// HealthChecker is a function that checks service health.
// A nil HealthChecker means the service is always considered ready.
type HealthChecker func() error
```

This means:
- It is not an interface; structs cannot "implement" it.
- It takes no arguments (no `context.Context` parameter).
- The correct usage is to pass a closure or a named function to `NewServer`.

## Implementation

Instead of a struct with a `Check(ctx) error` method, `internal/health/checker.go`
provides a factory function:

```go
func NewDBChecker(db *apikit.DB) apikit.HealthChecker {
    return func() error {
        return db.Ping(context.Background())
    }
}
```

This returns a closure that satisfies the `func() error` type signature.
The `context.Background()` is used internally since the `HealthChecker`
type does not accept a context parameter.
