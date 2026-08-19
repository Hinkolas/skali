package authz

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Hinkolas/skali/internal/auth"
	"github.com/Hinkolas/skali/internal/store"
)

// ErrNotFound: the project or environment does not exist. Callers answer it
// exactly like "not visible" so non-members learn nothing.
var ErrNotFound = errors.New("authz: not found")

// Settings are the environment's server-side knobs as stored.
type Settings struct {
	MaxRole      Role
	DeployPolicy string
	PromoteFrom  []string
	Priority     string
}

// EnvironmentGrant is one user's standing on one environment.
type EnvironmentGrant struct {
	ID   uuid.UUID
	Name string
	// Role is the effective role (Effective), the value every check uses.
	Role Role
	// Cell is the explicit override when one exists.
	Cell     *Role
	Settings Settings
}

// Locked: the environment is listed by name only.
func (e *EnvironmentGrant) Locked() bool { return e.Role == None }

// Grant is one user's standing on one project and all of its environments.
type Grant struct {
	ProjectID     uuid.UUID
	ProjectName   string
	InstanceAdmin bool
	// Member: a membership row exists. Instance admins are visible everywhere
	// without one.
	Member bool
	// ProjectRole is the membership role (Admin for instance admins, None
	// for non-members).
	ProjectRole  Role
	Environments map[uuid.UUID]*EnvironmentGrant
	deployerCell bool
}

// Visible: the project exists for this user (lists include it, direct
// access does not answer 404).
func (g *Grant) Visible() bool { return g.InstanceAdmin || g.Member }

// Deployer: may submit definitions, write the draft, push to the project's
// registry repositories. A project role of deploy or above, or any cell of
// deploy or above; the project role counts even when every environment is
// capped or none exists yet, so the first definition can land.
func (g *Grant) Deployer() bool {
	return g.InstanceAdmin || g.ProjectRole >= Deploy || g.deployerCell
}

// MayCreateEnvironments: project maintain and up.
func (g *Grant) MayCreateEnvironments() bool {
	return g.InstanceAdmin || g.ProjectRole >= Maintain
}

// Environment returns the grant for one environment of the project.
func (g *Grant) Environment(id uuid.UUID) (*EnvironmentGrant, bool) {
	e, ok := g.Environments[id]
	return e, ok
}

// EnvironmentByName returns the grant for one environment by name.
func (g *Grant) EnvironmentByName(name string) (*EnvironmentGrant, bool) {
	for _, e := range g.Environments {
		if e.Name == name {
			return e, true
		}
	}
	return nil, false
}

// Roles maps environment name to effective role name, the wire shape of
// project payloads.
func (g *Grant) Roles() map[string]string {
	roles := make(map[string]string, len(g.Environments))
	for _, e := range g.Environments {
		roles[e.Name] = e.Role.String()
	}
	return roles
}

// Resolver reads memberships, cells, and environment settings and turns
// them into grants.
type Resolver struct {
	st *store.Store
}

func New(st *store.Store) *Resolver { return &Resolver{st: st} }

// MayCreateProject: instance admins always, members when granted.
func (r *Resolver) MayCreateProject(user *store.User) bool {
	return user.Role == auth.RoleAdmin || user.CreateProjects
}

// Project resolves one project. ErrNotFound when the project does not
// exist; a non-member gets a grant with Visible() false.
func (r *Resolver) Project(ctx context.Context, user *store.User, projectID uuid.UUID) (*Grant, error) {
	project, err := r.st.GetProjectByID(ctx, projectID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("authz: get project: %w", err)
	}
	grants, err := r.load(ctx, user, []store.Project{project})
	if err != nil {
		return nil, err
	}
	return grants[projectID], nil
}

// Environment resolves the environment's project and returns both grants.
// ErrNotFound when the environment does not exist.
func (r *Resolver) Environment(ctx context.Context, user *store.User, environmentID uuid.UUID) (*Grant, *EnvironmentGrant, error) {
	env, err := r.st.GetEnvironmentByID(ctx, environmentID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, ErrNotFound
		}
		return nil, nil, fmt.Errorf("authz: get environment: %w", err)
	}
	grant, err := r.Project(ctx, user, env.ProjectID)
	if err != nil {
		return nil, nil, err
	}
	envGrant, ok := grant.Environment(env.ID)
	if !ok {
		return nil, nil, ErrNotFound
	}
	return grant, envGrant, nil
}

// All resolves every project the user can see, in name order, with grants.
func (r *Resolver) All(ctx context.Context, user *store.User) ([]store.Project, map[uuid.UUID]*Grant, error) {
	var (
		projects []store.Project
		err      error
	)
	if user.Role == auth.RoleAdmin {
		projects, err = r.st.ListProjects(ctx)
	} else {
		projects, err = r.st.ListProjectsForUser(ctx, user.ID)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("authz: list projects: %w", err)
	}
	grants, err := r.load(ctx, user, projects)
	if err != nil {
		return nil, nil, err
	}
	return projects, grants, nil
}

// DeployerAnywhere: deployer on at least one project, however obtained.
func (r *Resolver) DeployerAnywhere(ctx context.Context, user *store.User) (bool, error) {
	if user.Role == auth.RoleAdmin {
		return true, nil
	}
	deployer, err := r.st.UserIsDeployerAnywhere(ctx, user.ID)
	if err != nil {
		return false, fmt.Errorf("authz: deployer anywhere: %w", err)
	}
	return deployer, nil
}

// load builds the grants for the given projects: three reads (memberships,
// environments, cells) however many projects are asked for.
func (r *Resolver) load(ctx context.Context, user *store.User, projects []store.Project) (map[uuid.UUID]*Grant, error) {
	instanceAdmin := user.Role == auth.RoleAdmin
	ids := make([]uuid.UUID, len(projects))
	grants := make(map[uuid.UUID]*Grant, len(projects))
	for i, p := range projects {
		ids[i] = p.ID
		grants[p.ID] = &Grant{
			ProjectID:     p.ID,
			ProjectName:   p.Name,
			InstanceAdmin: instanceAdmin,
			Environments:  map[uuid.UUID]*EnvironmentGrant{},
		}
		if instanceAdmin {
			grants[p.ID].ProjectRole = Admin
		}
	}

	memberships, err := r.st.ListProjectMembersForUser(ctx, user.ID)
	if err != nil {
		return nil, fmt.Errorf("authz: list memberships: %w", err)
	}
	for _, m := range memberships {
		g, ok := grants[m.ProjectID]
		if !ok {
			continue
		}
		role, ok := ParseRole(m.Role)
		if !ok {
			return nil, fmt.Errorf("authz: unknown stored role %q", m.Role)
		}
		g.Member = true
		if !instanceAdmin {
			g.ProjectRole = role
		}
	}

	cells := map[uuid.UUID]Role{}
	if !instanceAdmin {
		rows, err := r.st.ListEnvironmentAccessForUser(ctx, user.ID)
		if err != nil {
			return nil, fmt.Errorf("authz: list cells: %w", err)
		}
		for _, c := range rows {
			role, ok := ParseRole(c.Role)
			if !ok {
				return nil, fmt.Errorf("authz: unknown stored role %q", c.Role)
			}
			cells[c.EnvironmentID] = role
			if g, ok := grants[c.ProjectID]; ok && role >= Deploy {
				g.deployerCell = true
			}
		}
	}

	environments, err := r.st.ListEnvironmentsForProjects(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("authz: list environments: %w", err)
	}
	for i := range environments {
		env := &environments[i]
		g := grants[env.ProjectID]
		settings, err := settingsOf(env)
		if err != nil {
			return nil, err
		}
		var cell *Role
		if role, ok := cells[env.ID]; ok {
			cell = &role
		}
		g.Environments[env.ID] = &EnvironmentGrant{
			ID:       env.ID,
			Name:     env.Name,
			Role:     Effective(instanceAdmin, g.Member, g.ProjectRole, cell, settings.MaxRole),
			Cell:     cell,
			Settings: settings,
		}
	}
	return grants, nil
}

// SettingsOf decodes the stored settings of an environment row.
func SettingsOf(env *store.Environment) (Settings, error) { return settingsOf(env) }

func settingsOf(env *store.Environment) (Settings, error) {
	maxRole, ok := ParseRole(env.MaxRole)
	if !ok {
		return Settings{}, fmt.Errorf("authz: unknown stored max_role %q", env.MaxRole)
	}
	promoteFrom := env.PromoteFrom
	if promoteFrom == nil {
		promoteFrom = []string{}
	}
	return Settings{
		MaxRole:      maxRole,
		DeployPolicy: env.DeployPolicy,
		PromoteFrom:  promoteFrom,
		Priority:     env.Priority,
	}, nil
}

// Required phrases a refusal: "maintain on environment production required".
func Required(min Role, scope, name string) string {
	return fmt.Sprintf("%s on %s %s required", min, scope, name)
}

// ErrForbidden is a refusal that names the role it would take.
type ErrForbidden struct {
	Required Role
	Scope    string
	Name     string
	Reason   string
}

func (e *ErrForbidden) Error() string {
	msg := Required(e.Required, e.Scope, e.Name)
	if e.Reason != "" {
		msg += ": " + e.Reason
	}
	return msg
}
