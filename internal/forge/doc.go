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
//
// The package also receives the forge's webhook deliveries: MountWebhookIngress mounts the
// public endpoint POST /hooks/forge/:slug, which checks a delivery against the workspace's
// FORGE_WEBHOOK_SECRET, records it once in the delivery log (InitSchema creates the table,
// StartDeliveryRetention prunes it) and relays it as hub.forge.* audit events. The routes that
// set and remove the secret and list the deliveries are part of RegisterRoutes. The hub only
// verifies, records and relays; it never acts on a delivery or calls the forge for it.
package forge
