package workspace

// DefaultSyncUpdateLocalRefFnForTest exposes the production sync fast-forward
// function to the external test package (spec 01, task 9). The external
// package is needed because the coexistence tests also use carrypatch, which
// imports workspace.
var DefaultSyncUpdateLocalRefFnForTest = defaultSyncUpdateLocalRefFn
