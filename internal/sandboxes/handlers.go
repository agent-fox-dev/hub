package sandboxes

import (
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/wsaccess"
)

// RegisterRoutes mounts the sandbox routes on api. A nil svc means the feature
// is off: the routes still exist and answer 404 sandboxes_disabled, so a
// caller can tell a disabled feature from a mistyped path.
func RegisterRoutes(api *echo.Group, svc *Service) {
	// The listing names no workspace, so a token bound to one may read it
	// (spec 33).
	wsaccess.AllowBoundTokens(api.GET("/sandbox-profiles", handleListProfiles(svc)))
}

// profileResponse is one profile as callers see it. It carries names only:
// the secret names and the env key names, never an env value, and nothing of
// the provider or the deployment limits.
type profileResponse struct {
	Name            string            `json:"name"`
	Default         bool              `json:"default"`
	Image           string            `json:"image"`
	Network         string            `json:"network"`
	TTLSeconds      int64             `json:"ttl_seconds"`
	Resources       resourcesResponse `json:"resources"`
	Secrets         []string          `json:"secrets"`
	OptionalSecrets []string          `json:"optional_secrets"`
	EnvKeys         []string          `json:"env_keys"`
	RequireLandlock bool              `json:"require_landlock"`
}

// resourcesResponse is a profile's resource bounds; zero means the provider's
// default.
type resourcesResponse struct {
	CPUs     float64 `json:"cpus"`
	MemoryMB int64   `json:"memory_mb"`
	PIDs     int64   `json:"pids"`
	DiskMB   int64   `json:"disk_mb"`
}

// listProfilesResponse is the body of GET /sandbox-profiles.
type listProfilesResponse struct {
	Profiles []profileResponse `json:"profiles"`
}

// handleListProfiles serves GET /sandbox-profiles from the profiles the
// service holds in memory. It calls no provider and reads no database, so it
// keeps answering when the engine is gone.
//
// The checks run in a fixed order: no credential is a 401 (apikit's
// middleware answers it first in production), a hub without [sandbox] answers
// every authenticated caller alike with 404, and only then is a PAT's scope
// checked.
func handleListProfiles(svc *Service) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := apikit.GetAuthInfo(c)
		if auth == nil {
			return apikit.WriteAPIError(c, http.StatusUnauthorized, "authentication required")
		}
		if svc == nil {
			return apikit.WriteAPIErrorWithType(c, http.StatusNotFound, "sandboxes are not enabled on this hub", "sandboxes_disabled")
		}
		if !canReadSandboxes(auth) {
			return apikit.WriteAPIError(c, http.StatusForbidden, "PAT requires sandboxes:read scope")
		}
		return c.JSON(http.StatusOK, newListProfilesResponse(svc.Profiles()))
	}
}

// newListProfilesResponse builds the listing: the profiles sorted by name,
// each list sorted ascending.
func newListProfilesResponse(profiles []ValidatedProfile) listProfilesResponse {
	out := make([]profileResponse, 0, len(profiles))
	for _, p := range profiles {
		out = append(out, profileResponse{
			Name:       p.Name,
			Default:    p.Default,
			Image:      p.Image,
			Network:    p.Network,
			TTLSeconds: int64(p.TTL / time.Second),
			Resources: resourcesResponse{
				CPUs:     p.Resources.CPUs,
				MemoryMB: p.Resources.MemoryMB,
				PIDs:     p.Resources.PIDs,
				DiskMB:   p.Resources.DiskMB,
			},
			Secrets:         sortedNames(p.Secrets),
			OptionalSecrets: sortedNames(p.OptionalSecrets),
			EnvKeys:         sortedNames(slices.Collect(maps.Keys(p.Env))),
			RequireLandlock: p.RequireLandlock,
		})
	}
	slices.SortFunc(out, func(a, b profileResponse) int { return strings.Compare(a.Name, b.Name) })
	return listProfilesResponse{Profiles: out}
}

// sortedNames returns a sorted copy of names, which renders as a JSON array
// even when it is empty.
func sortedNames(names []string) []string {
	out := append([]string{}, names...)
	slices.Sort(out)
	return out
}
