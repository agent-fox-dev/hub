package mergequeue

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"
)

// errAuthHandled is a sentinel error returned by requireAuth when the
// auth error response has already been written to the client. Handlers
// should return nil (not the error itself) after receiving this.
var errAuthHandled = errors.New("auth error already handled")

// specRefPattern matches source_ref values of the form "spec/<id>-<rest>".
// The first capture group is the spec ID.
var specRefPattern = regexp.MustCompile(`^spec/(\d+)(?:-|$)`)

// submitMergeRequest is the request body for POST /merges.
type submitMergeRequest struct {
	TargetBranch string `json:"target_branch"`
	SourceRef    string `json:"source_ref"`
}

// mergeJobResponse builds a JSON-serializable response for a MergeJob.
// The nonce field is always excluded from responses.
func mergeJobResponse(job *MergeJob) map[string]interface{} {
	resp := map[string]interface{}{
		"id":             job.ID,
		"workspace_slug": job.WorkspaceSlug,
		"target_branch":  job.TargetBranch,
		"source_ref":     job.SourceRef,
		"status":         job.Status,
		"retry_count":    job.RetryCount,
		"available_at":   job.AvailableAt,
		"submitted_by":   job.SubmittedBy,
		"created_at":     job.CreatedAt,
		"updated_at":     job.UpdatedAt,
	}

	if job.CampaignID.Valid {
		resp["campaign_id"] = job.CampaignID.String
	} else {
		resp["campaign_id"] = nil
	}

	if job.SpecID.Valid {
		resp["spec_id"] = job.SpecID.String
	} else {
		resp["spec_id"] = nil
	}

	if job.RejectionReason.Valid {
		resp["rejection_reason"] = job.RejectionReason.String
	} else {
		resp["rejection_reason"] = nil
	}

	if job.BaseSHA.Valid {
		resp["base_sha"] = job.BaseSHA.String
	} else {
		resp["base_sha"] = nil
	}

	if job.MergedSHA.Valid {
		resp["merged_sha"] = job.MergedSHA.String
	} else {
		resp["merged_sha"] = nil
	}

	if job.ConflictDetails.Valid {
		// Deserialize from TEXT to native JSON array per REQ-15.1.
		var files []string
		if err := json.Unmarshal([]byte(job.ConflictDetails.String), &files); err == nil {
			resp["conflict_details"] = files
		} else {
			// Malformed JSON: log with merge_job_id and return null per REQ-15.E1.
			log.Printf("mergequeue: failed to parse conflict_details for job %s: %v", job.ID, err)
			resp["conflict_details"] = nil
		}
	} else {
		resp["conflict_details"] = nil
	}

	// check_output is included in single-item responses but omitted from list.
	if job.CheckOutput.Valid {
		resp["check_output"] = job.CheckOutput.String
	} else {
		resp["check_output"] = nil
	}

	return resp
}

// mergeJobListItem builds a response for a MergeJob in list context.
// check_output is omitted from list responses per REQ-11.1.
func mergeJobListItem(job *MergeJob) map[string]interface{} {
	resp := mergeJobResponse(job)
	delete(resp, "check_output")
	return resp
}

// RegisterMergeRoutes registers merge queue HTTP routes on the given API group.
// The queue parameter is optional — when non-nil, submit and requeue handlers
// call queue.Notify() to wake the worker goroutine immediately.
func RegisterMergeRoutes(api *echo.Group, db *sql.DB, queue *Queue) error {
	ws := api.Group("/workspaces/:slug")
	ws.POST("/merges", handleSubmitMerge(db, queue))
	ws.GET("/merges", handleListMerges(db))
	ws.GET("/merges/:id", handleGetMerge(db))
	ws.DELETE("/merges/:id", handleCancelMerge(db))
	ws.POST("/merges/:id/requeue", handleRequeueMerge(db, queue))
	return nil
}

// requireAuth extracts and validates auth info from the context.
// On failure it writes the HTTP error response and returns errAuthHandled.
// Callers must check err != nil and return nil to the framework.
func requireAuth(c echo.Context, scope string) (*apikit.AuthInfo, error) {
	auth := apikit.GetAuthInfo(c)
	if auth == nil {
		apikit.WriteAPIError(c, http.StatusUnauthorized, "authentication required")
		return nil, errAuthHandled
	}

	// Admin tokens and API keys have full access.
	if auth.CredentialType == "admin_token" || auth.CredentialType == "api_key" {
		return auth, nil
	}

	// PATs must have the required scope.
	if auth.CredentialType == "pat" {
		if !slices.Contains(auth.Permissions, scope) {
			apikit.WriteAPIError(c, http.StatusForbidden,
				"PAT requires "+scope+" scope")
			return nil, errAuthHandled
		}
	}

	return auth, nil
}

// workspaceExists checks whether a workspace with the given slug exists.
func workspaceExists(db *sql.DB, slug string) (bool, error) {
	var count int
	err := db.QueryRow("SELECT COUNT(*) FROM workspaces WHERE slug = ?", slug).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// handleSubmitMerge handles POST /api/v1/workspaces/:slug/merges.
func handleSubmitMerge(db *sql.DB, queue *Queue) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth, err := requireAuth(c, "merges:write")
		if err != nil {
			return nil // Error response already sent.
		}

		slug := c.Param("slug")

		// Verify workspace exists.
		wsExists, wsErr := workspaceExists(db, slug)
		if wsErr != nil {
			return apikit.WriteAPIError(c, http.StatusInternalServerError, "database error")
		}
		if !wsExists {
			return apikit.WriteAPIError(c, http.StatusNotFound, "workspace not found")
		}

		// Parse request body.
		var req submitMergeRequest
		if bindErr := json.NewDecoder(c.Request().Body).Decode(&req); bindErr != nil {
			return apikit.WriteAPIError(c, http.StatusBadRequest, "invalid request body")
		}

		if req.TargetBranch == "" {
			return apikit.WriteAPIError(c, http.StatusBadRequest, "target_branch is required")
		}
		if req.SourceRef == "" {
			return apikit.WriteAPIError(c, http.StatusBadRequest, "source_ref is required")
		}

		// Check duplicate active job.
		existing, findErr := FindActiveJob(db, slug, req.SourceRef)
		if findErr != nil {
			return apikit.WriteAPIError(c, http.StatusInternalServerError, "database error")
		}
		if existing != nil {
			return c.JSON(http.StatusConflict, map[string]interface{}{
				"error":           "merge already in progress for this source branch",
				"existing_job_id": existing.ID,
			})
		}

		// Infer campaign_id and spec_id from source_ref.
		var campaignID sql.NullString
		var specID sql.NullString
		if matches := specRefPattern.FindStringSubmatch(req.SourceRef); matches != nil {
			specNum := matches[1]
			specID = sql.NullString{String: specNum, Valid: true}
			// Look up campaign by matching workspace + spec.
			cid, found := findCampaignForSpec(db, slug, specNum)
			if found {
				campaignID = sql.NullString{String: cid, Valid: true}
			}
		}

		// Generate server-side nonce and job ID.
		jobID := uuid.New().String()
		nonce := uuid.New().String()
		now := apikit.NowUTC()

		job := &MergeJob{
			ID:            jobID,
			Nonce:         nonce,
			CampaignID:    campaignID,
			SpecID:        specID,
			WorkspaceSlug: slug,
			TargetBranch:  req.TargetBranch,
			SourceRef:     req.SourceRef,
			Status:        "prepared",
			RetryCount:    0,
			AvailableAt:   now,
			SubmittedBy:   auth.UserID,
			CreatedAt:     now,
			UpdatedAt:     now,
		}

		if insertErr := InsertMergeJob(db, job); insertErr != nil {
			return apikit.WriteAPIError(c, http.StatusInternalServerError, "failed to insert merge job")
		}

		// Transition to queued.
		if transErr := UpdateStatus(db, jobID, "queued"); transErr != nil {
			return apikit.WriteAPIError(c, http.StatusInternalServerError, "failed to enqueue merge job")
		}
		job.Status = "queued"

		// Re-read to get updated timestamps.
		job, err = GetMergeJob(db, jobID)
		if err != nil || job == nil {
			return apikit.WriteAPIError(c, http.StatusInternalServerError, "failed to read merge job")
		}

		// Wake the worker goroutine to pick up the new job immediately.
		if queue != nil {
			queue.Notify()
		}

		return c.JSON(http.StatusAccepted, mergeJobResponse(job))
	}
}

// findCampaignForSpec looks up a campaign that includes the given spec
// in the specified workspace. Returns (campaignID, true) if found.
func findCampaignForSpec(db *sql.DB, workspaceSlug, specID string) (string, bool) {
	var campaignID string
	err := db.QueryRow(`
		SELECT c.id FROM campaigns c
		JOIN campaign_specs cs ON cs.campaign_id = c.id
		WHERE c.workspace_slug = ? AND cs.spec_id = ? AND c.status = 'active'
		LIMIT 1`,
		workspaceSlug, specID,
	).Scan(&campaignID)
	if err != nil {
		return "", false
	}
	return campaignID, true
}

// handleListMerges handles GET /api/v1/workspaces/:slug/merges.
func handleListMerges(db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		_, err := requireAuth(c, "merges:read")
		if err != nil {
			return nil
		}

		slug := c.Param("slug")

		// Parse query parameters.
		status := c.QueryParam("status")
		if status != "" {
			if !slices.Contains(ValidStatuses, status) {
				return apikit.WriteAPIError(c, http.StatusBadRequest,
					"invalid status value '"+status+"'; valid values: "+strings.Join(ValidStatuses, ", "))
			}
		}

		limitStr := c.QueryParam("limit")
		limit := 50
		if limitStr != "" {
			parsed, parseErr := strconv.Atoi(limitStr)
			if parseErr != nil {
				return apikit.WriteAPIError(c, http.StatusBadRequest, "invalid limit parameter")
			}
			if parsed > 100 {
				return apikit.WriteAPIError(c, http.StatusBadRequest, "limit must not exceed 100")
			}
			if parsed <= 0 {
				return apikit.WriteAPIError(c, http.StatusBadRequest, "limit must be positive")
			}
			limit = parsed
		}

		afterID := c.QueryParam("after")
		var afterTime string
		if afterID != "" {
			// Look up the cursor job's created_at.
			afterJob, afterErr := GetMergeJob(db, afterID)
			if afterErr != nil {
				return apikit.WriteAPIError(c, http.StatusInternalServerError, "database error")
			}
			if afterJob == nil {
				return apikit.WriteAPIError(c, http.StatusBadRequest, "after cursor references a non-existent job")
			}
			afterTime = afterJob.CreatedAt
		}

		opts := ListOptions{
			Status:    status,
			AfterID:   afterID,
			AfterTime: afterTime,
			Limit:     limit,
		}

		jobs, nextCursor, listErr := ListMergeJobs(db, slug, opts)
		if listErr != nil {
			return apikit.WriteAPIError(c, http.StatusInternalServerError, "database error")
		}

		items := make([]map[string]interface{}, len(jobs))
		for i := range jobs {
			items[i] = mergeJobListItem(&jobs[i])
		}

		var cursor interface{}
		if nextCursor != "" {
			cursor = nextCursor
		}

		return c.JSON(http.StatusOK, map[string]interface{}{
			"items":       items,
			"next_cursor": cursor,
		})
	}
}

// handleGetMerge handles GET /api/v1/workspaces/:slug/merges/:id.
func handleGetMerge(db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		_, err := requireAuth(c, "merges:read")
		if err != nil {
			return nil
		}

		slug := c.Param("slug")
		id := c.Param("id")

		job, getErr := GetMergeJob(db, id)
		if getErr != nil {
			return apikit.WriteAPIError(c, http.StatusInternalServerError, "database error")
		}
		if job == nil {
			return apikit.WriteAPIError(c, http.StatusNotFound, "merge job not found")
		}

		// Anti-enumeration: verify workspace matches.
		if job.WorkspaceSlug != slug {
			return apikit.WriteAPIError(c, http.StatusNotFound, "merge job not found")
		}

		return c.JSON(http.StatusOK, mergeJobResponse(job))
	}
}

// handleCancelMerge handles DELETE /api/v1/workspaces/:slug/merges/:id.
func handleCancelMerge(db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		_, err := requireAuth(c, "merges:write")
		if err != nil {
			return nil
		}

		slug := c.Param("slug")
		id := c.Param("id")

		job, getErr := GetMergeJob(db, id)
		if getErr != nil {
			return apikit.WriteAPIError(c, http.StatusInternalServerError, "database error")
		}
		if job == nil {
			return apikit.WriteAPIError(c, http.StatusNotFound, "merge job not found")
		}
		if job.WorkspaceSlug != slug {
			return apikit.WriteAPIError(c, http.StatusNotFound, "merge job not found")
		}

		if job.Status != "queued" {
			return c.JSON(http.StatusConflict, map[string]interface{}{
				"error":  "job is not in queued status; current status: " + job.Status,
				"status": job.Status,
			})
		}

		// Conditional update — only cancel if still queued (race safety).
		rows, condErr := ConditionalUpdateStatus(db, id, "queued", "cancelled")
		if condErr != nil {
			return apikit.WriteAPIError(c, http.StatusInternalServerError, "database error")
		}
		if rows == 0 {
			// Job status changed between read and update.
			updatedJob, _ := GetMergeJob(db, id)
			currentStatus := "unknown"
			if updatedJob != nil {
				currentStatus = updatedJob.Status
			}
			return c.JSON(http.StatusConflict, map[string]interface{}{
				"error":  "job is not in queued status; current status: " + currentStatus,
				"status": currentStatus,
			})
		}

		return c.NoContent(http.StatusNoContent)
	}
}

// handleRequeueMerge handles POST /api/v1/workspaces/:slug/merges/:id/requeue.
func handleRequeueMerge(db *sql.DB, queue *Queue) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth, err := requireAuth(c, "merges:write")
		if err != nil {
			return nil
		}

		slug := c.Param("slug")
		id := c.Param("id")

		job, getErr := GetMergeJob(db, id)
		if getErr != nil {
			return apikit.WriteAPIError(c, http.StatusInternalServerError, "database error")
		}
		if job == nil {
			return apikit.WriteAPIError(c, http.StatusNotFound, "merge job not found")
		}
		if job.WorkspaceSlug != slug {
			return apikit.WriteAPIError(c, http.StatusNotFound, "merge job not found")
		}

		if job.Status != "dead_letter" {
			return c.JSON(http.StatusConflict, map[string]interface{}{
				"error":  "job is not in dead_letter status; current status: " + job.Status,
				"status": job.Status,
			})
		}

		// Check duplicate guard for the same (workspace_slug, source_ref).
		existing, findErr := FindActiveJob(db, slug, job.SourceRef)
		if findErr != nil {
			return apikit.WriteAPIError(c, http.StatusInternalServerError, "database error")
		}
		if existing != nil {
			return c.JSON(http.StatusConflict, map[string]interface{}{
				"error":           "merge already in progress for this source branch",
				"existing_job_id": existing.ID,
			})
		}

		// Create a new merge job with fresh nonce.
		now := apikit.NowUTC()
		newJob := &MergeJob{
			ID:            uuid.New().String(),
			Nonce:         uuid.New().String(),
			CampaignID:    job.CampaignID,
			SpecID:        job.SpecID,
			WorkspaceSlug: job.WorkspaceSlug,
			TargetBranch:  job.TargetBranch,
			SourceRef:     job.SourceRef,
			Status:        "queued",
			RetryCount:    0,
			AvailableAt:   now,
			SubmittedBy:   auth.UserID,
			CreatedAt:     now,
			UpdatedAt:     now,
		}

		if insertErr := InsertMergeJob(db, newJob); insertErr != nil {
			return apikit.WriteAPIError(c, http.StatusInternalServerError, "failed to create requeued job")
		}

		// Wake the worker goroutine to pick up the requeued job immediately.
		if queue != nil {
			queue.Notify()
		}

		return c.JSON(http.StatusAccepted, mergeJobResponse(newJob))
	}
}
