package project

import "errors"

var (
	ErrProjectNotFound      = errors.New("project: not found")
	ErrEnvironmentNotFound  = errors.New("project: environment not found")
	ErrDraftNotFound        = errors.New("project: draft not found")
	ErrProjectNameTaken     = errors.New("project: name already in use")
	ErrEnvironmentNameTaken = errors.New("project: environment name already in use")
	ErrInvalidName          = errors.New("project: name must match ^[a-z][a-z0-9-]{0,62}$")
	ErrInvalidSourceMode    = errors.New("project: source mode must be managed or file")
	ErrInvalidFormat        = errors.New("project: format must be yaml or json")
	ErrNameMismatch         = errors.New("project: manifest name does not match the project name")
	// ErrVersionConflict is the optimistic-concurrency rejection: the caller's
	// expected draft version is stale and the submission must not be merged.
	ErrVersionConflict = errors.New("project: draft version conflict")
)
