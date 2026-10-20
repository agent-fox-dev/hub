// Package forge serves a workspace's repository on its forge (GitHub or GitLab)
// through the hub, using the credential the hub holds for the workspace and never
// one of the caller's own.
//
// For every request the package builds a fresh aflib issuex client from the
// workspace's own record: its git_url or, for the upstream target of a carry-patch
// workspace, its upstream_url, the stored git credential and an optional API URL
// variable. Only this package, and the packages under it, may import issuex; neither
// internal/cli nor cmd/afc may import this package in a build, so that the static afc
// binary stays free of the forge client; a test of internal/cli runs afc against the routes.
package forge
