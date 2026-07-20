package cli

import (
	"context"
	"encoding/json"
	"fmt"
)

// resolveOrgSlug resolves an organization slug to its UUID by listing
// the authenticated user's organizations and matching on slug.
//
// This uses the GET /user/orgs endpoint because apikit does not provide
// a direct slug-to-UUID lookup. The search is O(n) over the user's orgs.
//
// Returns ("", error) when:
//   - The API call fails (network error or HTTP error)
//   - The response cannot be decoded (unexpected data shape)
//   - No org matches the given slug
//   - The matched org has an empty/missing UUID
func resolveOrgSlug(ctx context.Context, client *wsClient, slug string) (string, error) {
	respBody, statusCode, err := client.doRequest(ctx, "GET", "/user/orgs", nil)
	if err != nil {
		return "", fmt.Errorf("failed to list organizations: %w", err)
	}
	if statusCode >= 400 {
		return "", fmt.Errorf("failed to list organizations: HTTP %d", statusCode)
	}

	// Decode as an array of org objects. If the response is not an array
	// (unexpected shape), json.Unmarshal will return an error.
	var orgs []struct {
		ID   string `json:"id"`
		Slug string `json:"slug"`
	}
	if err := json.Unmarshal(respBody, &orgs); err != nil {
		return "", fmt.Errorf("unexpected response format from organization listing")
	}

	// Find the org by slug.
	for _, org := range orgs {
		if org.Slug == slug {
			if org.ID == "" {
				return "", fmt.Errorf("organization '%s' has no valid UUID", slug)
			}
			return org.ID, nil
		}
	}

	return "", fmt.Errorf("organization '%s' not found", slug)
}
