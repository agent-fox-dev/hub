package teams

import "github.com/labstack/echo/v4"

// --- Error Envelope ---

// ErrorDetail is the inner error object in the nested error envelope.
type ErrorDetail struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ErrorResponse is the nested error envelope per server_foundation (spec 01):
// {"error": {"code": <int>, "message": "<string>"}}
//
// This matches the canonical error format from the master PRD (docs/01_prd.md).
// All team endpoint error responses use this structure, with the code field
// mirroring the HTTP status code.
type ErrorResponse struct {
	Error ErrorDetail `json:"error"`
}

// writeError writes a nested error envelope response with the given HTTP
// status code and message. The Content-Type is set to application/json
// automatically by Echo's c.JSON method.
func writeError(c echo.Context, code int, message string) error {
	return c.JSON(code, ErrorResponse{
		Error: ErrorDetail{
			Code:    code,
			Message: message,
		},
	})
}

// --- Team Response ---

// TeamResponse is the JSON representation of a team in API responses.
// All timestamps are formatted as UTC RFC3339 with microsecond precision
// (e.g. "2026-07-10T15:01:11.889182Z") via FormatTime.
type TeamResponse struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Slug      string  `json:"slug"`
	URL       *string `json:"url"`
	Status    string  `json:"status"`
	CreatedAt string  `json:"created_at"`
	UpdatedAt string  `json:"updated_at"`
}

// teamToResponse converts a Team domain model to a TeamResponse,
// formatting timestamps as RFC3339 UTC with microsecond precision.
func teamToResponse(t *Team) TeamResponse {
	return TeamResponse{
		ID:        t.ID,
		Name:      t.Name,
		Slug:      t.Slug,
		URL:       t.URL,
		Status:    t.Status,
		CreatedAt: FormatTime(t.CreatedAt),
		UpdatedAt: FormatTime(t.UpdatedAt),
	}
}

// --- Member Response ---

// MemberResponse is the JSON representation of a team member in API responses.
// The joined_at field is formatted as UTC RFC3339 with microsecond precision.
// The email and name fields reflect current user data from a fresh JOIN
// against the users table (not cached values).
type MemberResponse struct {
	UserID   string `json:"user_id"`
	TeamID   string `json:"team_id"`
	Email    string `json:"email"`
	Name     string `json:"name"`
	JoinedAt string `json:"joined_at"`
}

// memberToResponse converts a TeamMember domain model to a MemberResponse,
// formatting the joined_at timestamp as RFC3339 UTC with microsecond precision.
func memberToResponse(m *TeamMember) MemberResponse {
	return MemberResponse{
		UserID:   m.UserID,
		TeamID:   m.TeamID,
		Email:    m.Email,
		Name:     m.Name,
		JoinedAt: FormatTime(m.JoinedAt),
	}
}
