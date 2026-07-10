package teams

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

// Handler provides HTTP handlers for team management endpoints.
type Handler struct {
	store *Store
}

// NewHandler creates a new Handler backed by the given Store.
func NewHandler(store *Store) *Handler {
	return &Handler{store: store}
}

// RegisterRoutes registers all team routes on the given Echo group.
// The caller is responsible for applying auth and admin middleware to the group.
func (h *Handler) RegisterRoutes(g *echo.Group) {
	g.POST("", h.createTeam)
	g.GET("", h.listTeams)
	g.GET("/:id", h.getTeam)
	g.POST("/:id/archive", h.archiveTeam)
	g.POST("/:id/reactivate", h.reactivateTeam)
	g.DELETE("/:id", h.deleteTeam)
	g.POST("/:id/members", h.addMember)
	g.GET("/:id/members", h.listMembers)
}

// --- Response types ---

// ErrorDetail is the inner error object in the nested error envelope.
type ErrorDetail struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ErrorResponse is the nested error envelope per server_foundation (spec 01):
// {"error": {"code": <int>, "message": "<string>"}}
type ErrorResponse struct {
	Error ErrorDetail `json:"error"`
}

// TeamResponse is the JSON representation of a team in API responses.
type TeamResponse struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Slug      string  `json:"slug"`
	URL       *string `json:"url"`
	Status    string  `json:"status"`
	CreatedAt string  `json:"created_at"`
	UpdatedAt string  `json:"updated_at"`
}

// writeError writes a nested error envelope response.
func writeError(c echo.Context, code int, message string) error {
	return c.JSON(code, ErrorResponse{
		Error: ErrorDetail{
			Code:    code,
			Message: message,
		},
	})
}

// teamToResponse converts a Team domain model to a TeamResponse.
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

// validateUUID checks if the given string is a valid UUID.
func validateUUID(s string) error {
	if _, err := uuid.Parse(s); err != nil {
		return ErrInvalidIDFormat
	}
	return nil
}

// --- Create team handler ---

// CreateTeamRequest is the JSON request body for POST /api/v1/teams.
// Pointer fields are used to distinguish missing from empty values.
type CreateTeamRequest struct {
	Name *string `json:"name"`
	Slug *string `json:"slug"`
	URL  *string `json:"url"`
}

func (h *Handler) createTeam(c echo.Context) error {
	var req CreateTeamRequest

	// Use json.NewDecoder directly for consistent parsing regardless of Content-Type.
	decoder := json.NewDecoder(c.Request().Body)
	if err := decoder.Decode(&req); err != nil {
		return writeError(c, http.StatusBadRequest, ErrInvalidRequestBody.Error())
	}

	// Check required fields are present.
	if req.Name == nil || req.Slug == nil {
		return writeError(c, http.StatusUnprocessableEntity, ErrMissingRequired.Error())
	}

	// Trim and validate name.
	name := strings.TrimSpace(*req.Name)
	if err := ValidateName(name); err != nil {
		return writeError(c, http.StatusUnprocessableEntity, err.Error())
	}

	// Validate slug.
	if err := ValidateSlug(*req.Slug); err != nil {
		return writeError(c, http.StatusUnprocessableEntity, err.Error())
	}

	// Validate URL if provided and non-empty.
	var urlVal *string
	if req.URL != nil && *req.URL != "" {
		if err := ValidateURL(*req.URL); err != nil {
			return writeError(c, http.StatusUnprocessableEntity, err.Error())
		}
		urlVal = req.URL
	}

	// Create the team.
	team, err := h.store.CreateTeam(name, *req.Slug, urlVal)
	if err != nil {
		if errors.Is(err, ErrTeamNameExists) {
			return writeError(c, http.StatusConflict, err.Error())
		}
		if errors.Is(err, ErrTeamSlugExists) {
			return writeError(c, http.StatusConflict, err.Error())
		}
		return writeError(c, http.StatusInternalServerError, "internal server error")
	}

	return c.JSON(http.StatusCreated, teamToResponse(team))
}

// --- Stub handlers for endpoints not yet implemented ---

func (h *Handler) listTeams(c echo.Context) error {
	includeArchived := c.QueryParam("include_archived") == "true"

	teamsList, err := h.store.ListTeams(includeArchived)
	if err != nil {
		return writeError(c, http.StatusInternalServerError, "internal server error")
	}

	// Build response array; never return null for an empty list.
	responses := make([]TeamResponse, 0, len(teamsList))
	for i := range teamsList {
		responses = append(responses, teamToResponse(&teamsList[i]))
	}

	return c.JSON(http.StatusOK, responses)
}

func (h *Handler) getTeam(c echo.Context) error {
	id := c.Param("id")
	if err := validateUUID(id); err != nil {
		return writeError(c, http.StatusBadRequest, ErrInvalidIDFormat.Error())
	}

	team, err := h.store.GetTeamByID(id)
	if err != nil {
		if errors.Is(err, ErrTeamNotFound) {
			return writeError(c, http.StatusNotFound, ErrTeamNotFound.Error())
		}
		return writeError(c, http.StatusInternalServerError, "internal server error")
	}

	return c.JSON(http.StatusOK, teamToResponse(team))
}

func (h *Handler) archiveTeam(c echo.Context) error {
	id := c.Param("id")
	if err := validateUUID(id); err != nil {
		return writeError(c, http.StatusBadRequest, ErrInvalidIDFormat.Error())
	}

	// Fetch current team to check its status.
	team, err := h.store.GetTeamByID(id)
	if err != nil {
		if errors.Is(err, ErrTeamNotFound) {
			return writeError(c, http.StatusNotFound, ErrTeamNotFound.Error())
		}
		return writeError(c, http.StatusInternalServerError, "internal server error")
	}

	if team.Status == "archived" {
		return writeError(c, http.StatusConflict, ErrTeamAlreadyArchived.Error())
	}

	updated, err := h.store.UpdateTeamStatus(id, "archived")
	if err != nil {
		return writeError(c, http.StatusInternalServerError, "internal server error")
	}

	return c.JSON(http.StatusOK, teamToResponse(updated))
}

func (h *Handler) reactivateTeam(c echo.Context) error {
	id := c.Param("id")
	if err := validateUUID(id); err != nil {
		return writeError(c, http.StatusBadRequest, ErrInvalidIDFormat.Error())
	}

	// Fetch current team to check its status.
	team, err := h.store.GetTeamByID(id)
	if err != nil {
		if errors.Is(err, ErrTeamNotFound) {
			return writeError(c, http.StatusNotFound, ErrTeamNotFound.Error())
		}
		return writeError(c, http.StatusInternalServerError, "internal server error")
	}

	if team.Status == "active" {
		return writeError(c, http.StatusConflict, ErrTeamAlreadyActive.Error())
	}

	updated, err := h.store.UpdateTeamStatus(id, "active")
	if err != nil {
		return writeError(c, http.StatusInternalServerError, "internal server error")
	}

	return c.JSON(http.StatusOK, teamToResponse(updated))
}

func (h *Handler) deleteTeam(c echo.Context) error {
	return writeError(c, http.StatusNotImplemented, "not implemented")
}

func (h *Handler) addMember(c echo.Context) error {
	return writeError(c, http.StatusNotImplemented, "not implemented")
}

func (h *Handler) listMembers(c echo.Context) error {
	return writeError(c, http.StatusNotImplemented, "not implemented")
}
