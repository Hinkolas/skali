package project

import "errors"

var (
	ErrProjectNotFound      = errors.New("project: not found")
	ErrEnvironmentNotFound  = errors.New("project: environment not found")
	ErrDraftNotFound        = errors.New("project: draft not found")
	ErrProjectNameTaken     = errors.New("project: name already in use")
	ErrEnvironmentNameTaken = errors.New("project: environment name already in use")
	ErrInvalidName          = errors.New("project: invalid name")
	ErrInvalidSourceMode    = errors.New("project: source mode must be managed or file")
	ErrInvalidFormat        = errors.New("project: format must be yaml or json")
	ErrNameMismatch         = errors.New("project: manifest name does not match the project name")
	ErrUserNotFound         = errors.New("project: user not found")
	// ErrProjectHasEnvironments: a project is deleted only after every
	// environment is purged; deleting the rows underneath live namespaces
	// would orphan running workloads.
	ErrProjectHasEnvironments = errors.New("project: environments still exist")
	// ErrNotMember: a cell needs a membership to hang off.
	ErrNotMember       = errors.New("project: user is not a member of the project")
	ErrInvalidRole     = errors.New("project: invalid role")
	ErrInvalidSettings = errors.New("project: invalid environment settings")
	// ErrVersionConflict is the optimistic-concurrency rejection: the caller's
	// expected draft version is stale and the submission must not be merged.
	ErrVersionConflict = errors.New("project: draft version conflict")
)
