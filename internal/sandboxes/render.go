package sandboxes

// leaseResponse is a lease as callers see it. It has no token, no token hash
// and no environment values: the fixed environment is stored for the sandbox
// gateway and no response returns it. The last four fields are null until set.
type leaseResponse struct {
	ID              string            `json:"id"`
	Workspace       string            `json:"workspace"`
	Profile         string            `json:"profile"`
	Purpose         string            `json:"purpose"`
	State           string            `json:"state"`
	ExpiresAt       string            `json:"expires_at"`
	Image           string            `json:"image"`
	Network         string            `json:"network"`
	Resources       resourcesResponse `json:"resources"`
	RequireLandlock bool              `json:"require_landlock"`
	Secrets         []string          `json:"secrets"`
	OptionalSecrets []string          `json:"optional_secrets"`
	Labels          map[string]string `json:"labels"`
	CreatedBy       string            `json:"created_by"`
	CreatedAt       string            `json:"created_at"`
	UpdatedAt       string            `json:"updated_at"`
	SandboxID       *string           `json:"sandbox_id"`
	Error           *string           `json:"error"`
	EndedAt         *string           `json:"ended_at"`
	EndReason       *string           `json:"end_reason"`
}

// issuedLeaseResponse is the body of the POST that issued a lease: the lease
// and its token, which no other response ever carries.
type issuedLeaseResponse struct {
	leaseResponse
	Token string `json:"token"`
}

// renderLease turns a lease into its response. Lists and maps are never nil,
// so they render as [] and {}.
func renderLease(l *Lease) leaseResponse {
	return leaseResponse{
		ID:              l.ID,
		Workspace:       l.Workspace,
		Profile:         l.Profile,
		Purpose:         l.Purpose,
		State:           l.State,
		ExpiresAt:       l.ExpiresAt,
		Image:           l.Image,
		Network:         l.Network,
		Resources:       resourcesResponse{CPUs: l.Resources.CPUs, MemoryMB: l.Resources.MemoryMB, PIDs: l.Resources.PIDs, DiskMB: l.Resources.DiskMB},
		RequireLandlock: l.RequireLandlock,
		Secrets:         nonNilStrings(l.Secrets),
		OptionalSecrets: nonNilStrings(l.OptionalSecrets),
		Labels:          nonNilMap(l.Labels),
		CreatedBy:       l.CreatedBy,
		CreatedAt:       l.CreatedAt,
		UpdatedAt:       l.UpdatedAt,
		SandboxID:       l.SandboxID,
		Error:           l.Error,
		EndedAt:         l.EndedAt,
		EndReason:       l.EndReason,
	}
}
