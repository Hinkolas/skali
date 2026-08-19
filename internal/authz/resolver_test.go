package authz

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/auth"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/testdb"
)

type fixture struct {
	t  *testing.T
	st *store.Store
	r  *Resolver
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	st := store.NewStore(testdb.New(t))
	return &fixture{t: t, st: st, r: New(st)}
}

func (f *fixture) user(email, role string) *store.User {
	f.t.Helper()
	u, err := auth.CreateUser(context.Background(), f.st, email, "", "hunter2hunter2", role)
	require.NoError(f.t, err)
	return u
}

func (f *fixture) project(name string) store.Project {
	f.t.Helper()
	p, err := f.st.CreateProject(context.Background(), store.CreateProjectParams{
		ID: uuid.New(), Name: name, SourceMode: "managed",
	})
	require.NoError(f.t, err)
	return p
}

func (f *fixture) environment(projectID uuid.UUID, name, maxRole string) store.Environment {
	f.t.Helper()
	e, err := f.st.CreateEnvironment(context.Background(), store.CreateEnvironmentParams{
		ID: uuid.New(), ProjectID: projectID, Name: name, MaxRole: maxRole, Priority: PriorityNormal,
	})
	require.NoError(f.t, err)
	return e
}

func (f *fixture) member(projectID, userID uuid.UUID, role Role) {
	f.t.Helper()
	_, err := f.st.UpsertProjectMember(context.Background(), store.UpsertProjectMemberParams{
		ProjectID: projectID, UserID: userID, Role: role.String(),
	})
	require.NoError(f.t, err)
}

func (f *fixture) cell(env store.Environment, userID uuid.UUID, role Role) {
	f.t.Helper()
	_, err := f.st.UpsertEnvironmentAccess(context.Background(), store.UpsertEnvironmentAccessParams{
		EnvironmentID: env.ID, ProjectID: env.ProjectID, UserID: userID, Role: role.String(),
	})
	require.NoError(f.t, err)
}

func TestResolverGrid(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	alice := f.user("alice@example.com", auth.RoleMember)
	bob := f.user("bob@example.com", auth.RoleMember)
	carol := f.user("carol@example.com", auth.RoleMember)
	dave := f.user("dave@example.com", auth.RoleMember)
	root := f.user("root@example.com", auth.RoleAdmin)

	site := f.project("site")
	staging := f.environment(site.ID, "staging", "admin")
	production := f.environment(site.ID, "production", "read")
	featX := f.environment(site.ID, "feat-x", "admin")

	f.member(site.ID, alice.ID, Admin)
	f.member(site.ID, bob.ID, Maintain)
	f.member(site.ID, carol.ID, Read)
	f.cell(production, bob.ID, Read)
	f.cell(featX, carol.ID, Deploy)

	roleOf := func(u *store.User, env store.Environment) Role {
		grant, envGrant, err := f.r.Environment(ctx, u, env.ID)
		require.NoError(t, err)
		require.True(t, grant.Visible())
		return envGrant.Role
	}

	// alice (admin): never capped.
	require.Equal(t, Admin, roleOf(alice, staging))
	require.Equal(t, Admin, roleOf(alice, production))
	// bob (maintain): cell on production, ceiling would cap anyway.
	require.Equal(t, Maintain, roleOf(bob, staging))
	require.Equal(t, Read, roleOf(bob, production))
	require.Equal(t, Maintain, roleOf(bob, featX))
	// carol (read): cell up on feat-x.
	require.Equal(t, Read, roleOf(carol, staging))
	require.Equal(t, Read, roleOf(carol, production))
	require.Equal(t, Deploy, roleOf(carol, featX))
	// root: instance admin.
	require.Equal(t, Admin, roleOf(root, production))

	// dave is not a member: the project is invisible.
	grant, err := f.r.Project(ctx, dave, site.ID)
	require.NoError(t, err)
	require.False(t, grant.Visible())
	require.False(t, grant.Deployer())
	_, envGrant, err := f.r.Environment(ctx, dave, production.ID)
	require.NoError(t, err)
	require.Equal(t, None, envGrant.Role)

	// Deployer predicate: carol's deploy cell makes her one; a read member
	// without cells is not.
	grant, err = f.r.Project(ctx, carol, site.ID)
	require.NoError(t, err)
	require.True(t, grant.Deployer())
	require.False(t, grant.MayCreateEnvironments())
	grant, err = f.r.Project(ctx, bob, site.ID)
	require.NoError(t, err)
	require.True(t, grant.Deployer())
	require.True(t, grant.MayCreateEnvironments())

	deployer, err := f.r.DeployerAnywhere(ctx, carol)
	require.NoError(t, err)
	require.True(t, deployer)
	deployer, err = f.r.DeployerAnywhere(ctx, dave)
	require.NoError(t, err)
	require.False(t, deployer)

	// Unknown ids.
	_, err = f.r.Project(ctx, alice, uuid.New())
	require.ErrorIs(t, err, ErrNotFound)
	_, _, err = f.r.Environment(ctx, alice, uuid.New())
	require.ErrorIs(t, err, ErrNotFound)
}

func TestResolverAllAndCeilingEverywhere(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	erin := f.user("erin@example.com", auth.RoleMember)
	root := f.user("root@example.com", auth.RoleAdmin)

	capped := f.project("capped")
	prod := f.environment(capped.ID, "production", "read")
	empty := f.project("empty")
	hidden := f.project("hidden")
	f.environment(hidden.ID, "production", "admin")

	f.member(capped.ID, erin.ID, Maintain)
	f.member(empty.ID, erin.ID, Deploy)

	projects, grants, err := f.r.All(ctx, erin)
	require.NoError(t, err)
	require.Len(t, projects, 2)
	require.Equal(t, "capped", projects[0].Name)
	require.Equal(t, "empty", projects[1].Name)
	require.NotContains(t, grants, hidden.ID)

	// A maintain member capped to read everywhere is still a deployer by
	// project role, as is a deploy member of a project without environments.
	require.Equal(t, Read, grants[capped.ID].Environments[prod.ID].Role)
	require.True(t, grants[capped.ID].Deployer())
	require.True(t, grants[empty.ID].Deployer())
	require.Equal(t, map[string]string{"production": "read"}, grants[capped.ID].Roles())

	projects, grants, err = f.r.All(ctx, root)
	require.NoError(t, err)
	require.Len(t, projects, 3)
	require.True(t, grants[hidden.ID].Visible())
	require.Equal(t, Admin, grants[hidden.ID].ProjectRole)

	require.True(t, f.r.MayCreateProject(root))
	require.False(t, f.r.MayCreateProject(erin))
	erin.CreateProjects = true
	require.True(t, f.r.MayCreateProject(erin))
}
