package campaign

import (
	"database/sql"
	"net/http"

	"github.com/labstack/echo/v4"
)

// Handler implements the campaign REST API endpoints.
type Handler struct {
	store         *Store
	scheduler     *Scheduler
	db            *sql.DB
	gitOps        GitOps
	rebaseEngine  *RebaseEngine
	authz         *Authz
	workspaceRoot string
}

// NewHandler creates a new campaign Handler.
func NewHandler(db *sql.DB) *Handler {
	store := NewStore(db)
	return &Handler{
		store:     store,
		scheduler: NewScheduler(store),
		db:        db,
	}
}

// RegisterRoutes registers campaign API routes on the given echo group.
// The group should be mounted at /api/v1.
func RegisterRoutes(g *echo.Group, db *sql.DB) error {
	h := NewHandler(db)

	campaigns := g.Group("/workspaces/:slug/campaigns")
	campaigns.POST("", h.createCampaign)
	campaigns.GET("", h.listCampaigns)
	campaigns.GET("/:id", h.getCampaign)
	campaigns.DELETE("/:id", h.cancelCampaign)
	campaigns.POST("/:id/specs/:spec_id/resolve", h.resolveSpec)

	return nil
}

func (h *Handler) createCampaign(c echo.Context) error {
	return c.JSON(http.StatusNotImplemented, map[string]string{
		"error": "not implemented",
	})
}

func (h *Handler) listCampaigns(c echo.Context) error {
	return c.JSON(http.StatusNotImplemented, map[string]string{
		"error": "not implemented",
	})
}

func (h *Handler) getCampaign(c echo.Context) error {
	return c.JSON(http.StatusNotImplemented, map[string]string{
		"error": "not implemented",
	})
}

func (h *Handler) cancelCampaign(c echo.Context) error {
	return c.JSON(http.StatusNotImplemented, map[string]string{
		"error": "not implemented",
	})
}

func (h *Handler) resolveSpec(c echo.Context) error {
	return c.JSON(http.StatusNotImplemented, map[string]string{
		"error": "not implemented",
	})
}
