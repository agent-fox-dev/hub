package teams

import "errors"

// Sentinel errors for the teams domain.
var (
	ErrTeamNameExists      = errors.New("team name already exists")
	ErrTeamSlugExists      = errors.New("team slug already exists")
	ErrTeamNotFound        = errors.New("team not found")
	ErrInvalidTeamName     = errors.New("invalid team name")
	ErrInvalidSlugFormat   = errors.New("invalid slug format")
	ErrInvalidURLFormat    = errors.New("invalid url format")
	ErrMissingRequired     = errors.New("missing required field")
	ErrInvalidRequestBody  = errors.New("invalid request body")
	ErrInvalidIDFormat     = errors.New("invalid id format")
	ErrTeamArchived        = errors.New("team is archived")
	ErrTeamAlreadyArchived = errors.New("team is already archived")
	ErrTeamAlreadyActive   = errors.New("team is already active")
	ErrArchiveBeforeDelete = errors.New("team must be archived before deletion")
	ErrUserNotFound        = errors.New("user not found")
)
