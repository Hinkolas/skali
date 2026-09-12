package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"math"
	"math/rand"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Hinkolas/skali/internal/artifactstore"
	"github.com/Hinkolas/skali/internal/auth"
	"github.com/Hinkolas/skali/internal/authz"
	"github.com/Hinkolas/skali/internal/backup"
	"github.com/Hinkolas/skali/internal/claim"
	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/config"
	"github.com/Hinkolas/skali/internal/dbstore"
	"github.com/Hinkolas/skali/internal/deploy"
	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/project"
	"github.com/Hinkolas/skali/internal/redact"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/utils"
	"github.com/Hinkolas/skali/internal/valuestore"
)

// runSeed fabricates a data-rich development installation for exercising
// the console: users with different instance roles, projects with several
// environments, memberships and environment cells, deployment history
// (successes, a failure, a rollback, restarts, backups, one deployment in
// flight), promoted revisions with provisioned database and bucket claims,
// and a week of usage, edge, and storage telemetry.
//
// Everything goes through the real services where one exists (users,
// drafts, environments, access, values, deployments, journal, claims), so
// the rows are shaped exactly like production writes them; only
// timestamps are rewritten afterwards to spread the history over --days.
// Cluster observation (health, pods, node list) lives in memory and is not
// seeded: an API-only skalid reports it as unknown.
//
// Seeded rows are recognizable (users under @seed.skali.local, a fixed set
// of project names, node names skali-seed-*), and --reset removes exactly
// those before seeding again.
func runSeed(args []string) error {
	fs := flag.NewFlagSet("seed", flag.ContinueOnError)
	reset := fs.Bool("reset", false, "remove previously seeded data first")
	days := fs.Int("days", 7, "history and telemetry span in days")
	password := fs.String("password", "seed-password", "password of every seeded user")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *days < 1 {
		return errors.New("--days must be at least 1")
	}
	if err := validateSeedPassword(*password); err != nil {
		return err
	}
	ctx := context.Background()

	cfg, err := config.Load[config.API](ctx)
	if err != nil {
		return err
	}
	pool, err := store.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	st := store.NewStore(pool)

	values, err := valuestore.New(st, cfg.AuthSecret)
	if err != nil {
		return err
	}
	artifacts := artifactstore.New(st)
	deploySvc := deploy.New(st, values, artifacts, "seed")
	// A kernel-shaped enqueuer keeps deployment runs open after promotion
	// so the seeder can journal the rollout the reconciler would have.
	deploySvc.SetEnqueuer(seedEnqueuer{})

	s := &seeder{
		st:       st,
		projects: project.New(st),
		values:   values,
		deploy:   deploySvc,
		journal:  journal.NewService(st, "seed"),
		db:       dbstore.New(st),
		now:      time.Now().UTC(),
		span:     time.Duration(*days) * 24 * time.Hour,
		rand:     rand.New(rand.NewSource(42)),
		password: *password,
	}
	if *reset {
		if err := s.reset(ctx); err != nil {
			return err
		}
	}
	if err := s.run(ctx); err != nil {
		return err
	}
	fmt.Printf("seeded %d users and %d projects; every seeded user logs in with password %q\n",
		len(seedUsers), len(seedProjects), *password)
	return nil
}

func validateSeedPassword(password string) error {
	if len(password) < 8 {
		return errors.New("--password must be at least 8 characters")
	}
	return nil
}

// seedEnqueuer stands in for the reconcile kernel: promotion leaves the
// run running with a pending rollout step, exactly as in production.
type seedEnqueuer struct{}

func (seedEnqueuer) Enqueue(uuid.UUID) {}

type seeder struct {
	st       *store.Store
	projects *project.Service
	values   *valuestore.Service
	deploy   *deploy.Service
	journal  *journal.Service
	db       *dbstore.Service
	now      time.Time
	span     time.Duration
	rand     *rand.Rand
	password string

	users map[string]*store.User
}

// --- Fixture definitions ---

type seedUser struct {
	email, name, role string
	createProjects    bool
}

// The first user is the instance admin to log in as.
var seedUsers = []seedUser{
	{email: "ada@seed.skali.local", name: "Ada Lovelace", role: auth.RoleAdmin},
	{email: "grace@seed.skali.local", name: "Grace Hopper", role: auth.RoleMember, createProjects: true},
	{email: "linus@seed.skali.local", name: "Linus Torvalds", role: auth.RoleMember},
	{email: "margaret@seed.skali.local", name: "Margaret Hamilton", role: auth.RoleMember},
	{email: "ken@seed.skali.local", name: "Ken Thompson", role: auth.RoleMember, createProjects: true},
	{email: "barbara@seed.skali.local", name: "Barbara Liskov", role: auth.RoleAdmin},
	{email: "dennis@seed.skali.local", name: "Dennis Ritchie", role: auth.RoleMember},
}

type seedProject struct {
	name, displayName string
	// creator is the email of the member who created it (project admin).
	creator string
	// manifests are the definition versions in the order they were
	// deployed; the last one is the current draft.
	manifests []string
	members   map[string]authz.Role
	envs      []seedEnv
}

type seedEnv struct {
	name     string
	settings *authz.Settings
	cells    map[string]authz.Role
	values   map[string]string
	// history is the run sequence, oldest first; ages are fractions of the
	// seeded span (0 = start of the span, 1 = now).
	history []seedRun
	// down marks an environment that was torn down after its history.
	down bool
	// metrics drives telemetry generation: applications with their base
	// load and replica count.
	metrics map[string]appLoad
}

type appLoad struct {
	cpuMillicores int64
	memoryBytes   int64
	replicas      int64
	// requestsPerMinute is the edge load per route; 0 renders no edge
	// series.
	requestsPerMinute int64
}

type runKind int

const (
	runDeploy runKind = iota
	runDeployFailed
	runRollback
	runRestart
	runBackup
	runRestoreFailed
	runDeployInFlight
)

type seedRun struct {
	kind  runKind
	age   float64 // fraction of the span before now
	actor string
	// manifest is the index into the project's manifests for deploys.
	manifest int
	// application scopes a restart.
	application string
}

const (
	gib = int64(1) << 30
	mib = int64(1) << 20
)

var seedProjects = []seedProject{
	{
		name:        "storefront",
		displayName: "Storefront",
		creator:     "ada@seed.skali.local",
		manifests:   []string{storefrontV1, storefrontV2, storefrontV3},
		members: map[string]authz.Role{
			"grace@seed.skali.local":    authz.Maintain,
			"linus@seed.skali.local":    authz.Deploy,
			"margaret@seed.skali.local": authz.Read,
			"dennis@seed.skali.local":   authz.Deploy,
		},
		envs: []seedEnv{
			{
				name: "production",
				settings: &authz.Settings{
					MaxRole: authz.Maintain, DeployPolicy: "promote-only",
					PromoteFrom: []string{"staging"}, Priority: "high",
				},
				cells: map[string]authz.Role{
					"linus@seed.skali.local":  authz.Read,
					"dennis@seed.skali.local": authz.None,
				},
				values: map[string]string{
					"APP_DOMAIN":     "shop.example.com",
					"SESSION_SECRET": "prod-session-secret-9f8e7d6c",
					"STRIPE_KEY":     "fixture-stripe-production",
					"SENTRY_DSN":     "https://abc@o1.ingest.sentry.io/1",
				},
				history: []seedRun{
					{kind: runDeploy, age: 0.95, actor: "ada@seed.skali.local", manifest: 0},
					{kind: runBackup, age: 0.80, actor: "ada@seed.skali.local"},
					{kind: runDeploy, age: 0.62, actor: "grace@seed.skali.local", manifest: 1},
					{kind: runRestart, age: 0.50, actor: "linus@seed.skali.local", application: "api"},
					{kind: runBackup, age: 0.40, actor: "ada@seed.skali.local"},
					{kind: runDeployFailed, age: 0.30, actor: "grace@seed.skali.local", manifest: 2},
					{kind: runRollback, age: 0.29, actor: "grace@seed.skali.local"},
					{kind: runDeploy, age: 0.12, actor: "ada@seed.skali.local", manifest: 2},
					{kind: runBackup, age: 0.05, actor: "ada@seed.skali.local"},
				},
				metrics: map[string]appLoad{
					"web":    {cpuMillicores: 180, memoryBytes: 320 * mib, replicas: 3, requestsPerMinute: 900},
					"api":    {cpuMillicores: 420, memoryBytes: 610 * mib, replicas: 3, requestsPerMinute: 2400},
					"worker": {cpuMillicores: 90, memoryBytes: 210 * mib, replicas: 1},
				},
			},
			{
				name: "staging",
				values: map[string]string{
					"APP_DOMAIN":     "shop-staging.example.com",
					"SESSION_SECRET": "staging-session-secret",
					"STRIPE_KEY":     "fixture-stripe-staging",
					"SENTRY_DSN":     "https://abc@o1.ingest.sentry.io/2",
				},
				history: []seedRun{
					{kind: runDeploy, age: 0.97, actor: "grace@seed.skali.local", manifest: 0},
					{kind: runDeploy, age: 0.70, actor: "grace@seed.skali.local", manifest: 1},
					{kind: runDeploy, age: 0.45, actor: "linus@seed.skali.local", manifest: 2},
					{kind: runRestoreFailed, age: 0.38, actor: "grace@seed.skali.local"},
					{kind: runRestart, age: 0.20, actor: "linus@seed.skali.local", application: "web"},
					{kind: runDeploy, age: 0.02, actor: "grace@seed.skali.local", manifest: 2},
				},
				metrics: map[string]appLoad{
					"web":    {cpuMillicores: 40, memoryBytes: 180 * mib, replicas: 1, requestsPerMinute: 60},
					"api":    {cpuMillicores: 70, memoryBytes: 260 * mib, replicas: 1, requestsPerMinute: 150},
					"worker": {cpuMillicores: 15, memoryBytes: 120 * mib, replicas: 1},
				},
			},
			{
				name:     "preview",
				settings: &authz.Settings{MaxRole: authz.Admin, DeployPolicy: "direct", Priority: "normal"},
				values: map[string]string{
					"APP_DOMAIN":     "preview.shop.example.com",
					"SESSION_SECRET": "preview-secret",
					"STRIPE_KEY":     "sk_test_preview",
					"SENTRY_DSN":     "",
				},
				history: []seedRun{
					{kind: runDeploy, age: 0.55, actor: "dennis@seed.skali.local", manifest: 2},
					{kind: runDeployInFlight, age: 0.0, actor: "dennis@seed.skali.local", manifest: 2},
				},
				metrics: map[string]appLoad{
					"web":    {cpuMillicores: 25, memoryBytes: 150 * mib, replicas: 1, requestsPerMinute: 8},
					"api":    {cpuMillicores: 30, memoryBytes: 200 * mib, replicas: 1, requestsPerMinute: 12},
					"worker": {cpuMillicores: 10, memoryBytes: 100 * mib, replicas: 1},
				},
			},
		},
	},
	{
		name:        "analytics",
		displayName: "Analytics Pipeline",
		creator:     "ken@seed.skali.local",
		manifests:   []string{analyticsV1, analyticsV2},
		members: map[string]authz.Role{
			"ada@seed.skali.local":   authz.Admin,
			"grace@seed.skali.local": authz.Deploy,
			"linus@seed.skali.local": authz.Maintain,
		},
		envs: []seedEnv{
			{
				name: "production",
				settings: &authz.Settings{
					MaxRole: authz.Admin, DeployPolicy: "promote-only",
					PromoteFrom: []string{"staging"}, Priority: "normal",
				},
				values: map[string]string{
					"APP_DOMAIN":    "analytics.example.com",
					"INGEST_TOKEN":  "ingest-token-prod-4d5e6f",
					"RETENTION_DAY": "90",
				},
				history: []seedRun{
					{kind: runDeploy, age: 0.90, actor: "ken@seed.skali.local", manifest: 0},
					{kind: runDeploy, age: 0.35, actor: "ken@seed.skali.local", manifest: 1},
					{kind: runBackup, age: 0.10, actor: "linus@seed.skali.local"},
				},
				metrics: map[string]appLoad{
					"ingest":    {cpuMillicores: 850, memoryBytes: 1200 * mib, replicas: 4, requestsPerMinute: 12000},
					"dashboard": {cpuMillicores: 120, memoryBytes: 380 * mib, replicas: 2, requestsPerMinute: 300},
					"scheduler": {cpuMillicores: 35, memoryBytes: 140 * mib, replicas: 1},
				},
			},
			{
				name: "staging",
				values: map[string]string{
					"APP_DOMAIN":    "analytics-staging.example.com",
					"INGEST_TOKEN":  "ingest-token-staging",
					"RETENTION_DAY": "7",
				},
				history: []seedRun{
					{kind: runDeploy, age: 0.92, actor: "ken@seed.skali.local", manifest: 0},
					{kind: runDeploy, age: 0.40, actor: "grace@seed.skali.local", manifest: 1},
					{kind: runDeployFailed, age: 0.15, actor: "grace@seed.skali.local", manifest: 1},
				},
				metrics: map[string]appLoad{
					"ingest":    {cpuMillicores: 90, memoryBytes: 400 * mib, replicas: 1, requestsPerMinute: 400},
					"dashboard": {cpuMillicores: 30, memoryBytes: 220 * mib, replicas: 1, requestsPerMinute: 20},
					"scheduler": {cpuMillicores: 10, memoryBytes: 90 * mib, replicas: 1},
				},
			},
		},
	},
	{
		name:        "docs",
		displayName: "Documentation Site",
		creator:     "ada@seed.skali.local",
		manifests:   []string{docsV1},
		members: map[string]authz.Role{
			"margaret@seed.skali.local": authz.Maintain,
			"barbara@seed.skali.local":  authz.Admin,
		},
		envs: []seedEnv{
			{
				name:   "production",
				values: map[string]string{"APP_DOMAIN": "docs.example.com"},
				history: []seedRun{
					{kind: runDeploy, age: 0.85, actor: "margaret@seed.skali.local", manifest: 0},
					{kind: runDeploy, age: 0.50, actor: "margaret@seed.skali.local", manifest: 0},
				},
				metrics: map[string]appLoad{
					"site": {cpuMillicores: 12, memoryBytes: 48 * mib, replicas: 2, requestsPerMinute: 500},
				},
			},
			{
				name:   "legacy",
				values: map[string]string{"APP_DOMAIN": "old-docs.example.com"},
				history: []seedRun{
					{kind: runDeploy, age: 0.98, actor: "ada@seed.skali.local", manifest: 0},
				},
				down: true,
			},
		},
	},
	{
		name:        "mailer",
		displayName: "Transactional Mailer",
		creator:     "grace@seed.skali.local",
		manifests:   []string{mailerV1},
		members: map[string]authz.Role{
			"ada@seed.skali.local":    authz.Admin,
			"dennis@seed.skali.local": authz.Read,
		},
		envs: []seedEnv{
			{
				name: "production",
				values: map[string]string{
					"APP_DOMAIN":   "mail.example.com",
					"SMTP_HOST":    "smtp.postmarkapp.com",
					"SMTP_USER":    "postmark-user",
					"SMTP_PASS":    "fixture-smtp-password",
					"WEBHOOK_KEY":  "fixture-webhook-key",
					"DEFAULT_FROM": "no-reply@example.com",
				},
				history: []seedRun{
					{kind: runDeploy, age: 0.75, actor: "grace@seed.skali.local", manifest: 0},
					{kind: runRestart, age: 0.33, actor: "ada@seed.skali.local", application: ""},
					{kind: runDeploy, age: 0.08, actor: "grace@seed.skali.local", manifest: 0},
				},
				metrics: map[string]appLoad{
					"api":   {cpuMillicores: 60, memoryBytes: 190 * mib, replicas: 2, requestsPerMinute: 700},
					"queue": {cpuMillicores: 140, memoryBytes: 300 * mib, replicas: 1},
				},
			},
		},
	},
	{
		name:        "sandbox",
		displayName: "Grace's Sandbox",
		creator:     "grace@seed.skali.local",
		manifests:   []string{sandboxV1},
		members:     map[string]authz.Role{},
		envs: []seedEnv{
			{
				name:   "dev",
				values: map[string]string{},
			},
		},
	},
}

// seedNodes are the telemetry-only nodes (the node list itself comes from
// cluster observation and stays empty without a cluster).
var seedNodes = []struct {
	name          string
	cpuAllocat    int64
	memAllocat    int64
	cpuBase       int64
	memBase       int64
	diskCapacity  int64
	diskImages    int64
	diskTemporary int64
}{
	{"skali-seed-01", 7800, 15 * gib, 2100, 6 * gib, 200 * gib, 12 * gib, 3 * gib},
	{"skali-seed-02", 15800, 31 * gib, 5400, 14 * gib, 500 * gib, 18 * gib, 7 * gib},
	{"skali-seed-03", 15800, 31 * gib, 4200, 11 * gib, 500 * gib, 16 * gib, 5 * gib},
}

// --- Seeding ---

func (s *seeder) run(ctx context.Context) error {
	if err := s.seedUsers(ctx); err != nil {
		return err
	}
	for _, p := range seedProjects {
		if err := s.seedProject(ctx, p); err != nil {
			return fmt.Errorf("seed project %s: %w", p.name, err)
		}
	}
	if err := s.seedNodeTelemetry(ctx); err != nil {
		return fmt.Errorf("seed node telemetry: %w", err)
	}
	return nil
}

func (s *seeder) seedUsers(ctx context.Context) error {
	s.users = make(map[string]*store.User, len(seedUsers))
	for _, u := range seedUsers {
		user, err := auth.CreateUser(ctx, s.st, u.email, u.name, s.password, u.role)
		if err != nil {
			if errors.Is(err, auth.ErrEmailTaken) {
				return fmt.Errorf("user %s already exists (run with --reset)", u.email)
			}
			return fmt.Errorf("create user %s: %w", u.email, err)
		}
		if u.createProjects {
			updated, err := auth.SetUserCreateProjects(ctx, s.st, user.ID, true)
			if err != nil {
				return err
			}
			user = &updated
		}
		s.users[u.email] = user
		fmt.Printf("user %-28s %-6s %s\n", u.email, u.role, u.name)
	}
	return nil
}

func (s *seeder) user(email string) uuid.UUID {
	u, ok := s.users[email]
	if !ok {
		panic("seed: unknown user " + email)
	}
	return u.ID
}

func (s *seeder) seedProject(ctx context.Context, p seedProject) error {
	if _, err := s.st.GetProjectByName(ctx, p.name); err == nil {
		return fmt.Errorf("project %s already exists (run with --reset)", p.name)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	proj, err := s.projects.Create(ctx, p.name, p.displayName, s.user(p.creator))
	if err != nil {
		return err
	}
	for email, role := range p.members {
		if _, err := s.projects.SetMember(ctx, proj.ID, s.user(email), role); err != nil {
			return fmt.Errorf("set member %s: %w", email, err)
		}
	}

	// Submit every manifest version so each has a definition version row;
	// the last submission is the current draft.
	versionIDs := make([]uuid.UUID, len(p.manifests))
	for i, manifest := range p.manifests {
		draft, err := s.projects.GetDraft(ctx, proj.ID)
		expected := int64(0)
		if err == nil {
			expected = draft.Version
		}
		if _, err := s.projects.SubmitDraft(ctx, proj.ID, project.DraftSubmission{
			Source: []byte(manifest), Format: "yaml", ExpectedVersion: expected,
		}); err != nil {
			return fmt.Errorf("submit manifest %d: %w", i+1, err)
		}
		row, err := s.st.GetProjectDraft(ctx, proj.ID)
		if err != nil {
			return err
		}
		versionIDs[i] = row.DefinitionVersionID
	}

	// Every environment exists before any settings apply: promote_from
	// validates against the project's other environments.
	envs := make([]*store.Environment, len(p.envs))
	for i, e := range p.envs {
		priority := ""
		if e.settings != nil {
			priority = e.settings.Priority
		}
		env, err := s.projects.CreateEnvironment(ctx, proj.ID, e.name, project.EnvironmentOptions{
			Creator: s.user(p.creator), Priority: priority,
		})
		if err != nil {
			return fmt.Errorf("environment %s: %w", e.name, err)
		}
		envs[i] = env
	}
	for i, e := range p.envs {
		if err := s.seedEnvironment(ctx, proj, envs[i], e, versionIDs); err != nil {
			return fmt.Errorf("environment %s: %w", e.name, err)
		}
	}
	// History may have advanced the draft to an older version; the newest
	// manifest is what the team is working on.
	if _, err := s.st.AdvanceProjectDraft(ctx, store.AdvanceProjectDraftParams{
		ProjectID: proj.ID, DefinitionVersionID: versionIDs[len(versionIDs)-1],
	}); err != nil {
		return err
	}
	if err := s.backdateProject(ctx, proj.ID, s.now.Add(-s.span)); err != nil {
		return err
	}
	fmt.Printf("project %-12s %d environments, %d manifest versions\n", p.name, len(p.envs), len(p.manifests))
	return nil
}

func (s *seeder) seedEnvironment(ctx context.Context, proj *store.Project, env *store.Environment, e seedEnv, versionIDs []uuid.UUID) error {
	if e.settings != nil {
		if _, err := s.projects.UpdateEnvironmentSettings(ctx, env.ID, *e.settings); err != nil {
			return fmt.Errorf("settings: %w", err)
		}
	}
	for email, role := range e.cells {
		if _, err := s.projects.SetEnvironmentAccess(ctx, env.ID, s.user(email), role); err != nil {
			return fmt.Errorf("cell %s: %w", email, err)
		}
	}

	candidateID := uuid.Nil
	if len(e.values) > 0 {
		candidate, err := s.values.Stage(ctx, env.ID, e.values)
		if err != nil {
			return fmt.Errorf("stage values: %w", err)
		}
		candidateID = candidate.ID
	}

	var lastGood uuid.UUID // the revision a rollback returns to
	var revisions []uuid.UUID
	for _, r := range e.history {
		at := s.now.Add(-time.Duration(float64(s.span) * r.age))
		switch r.kind {
		case runDeploy, runDeployFailed, runDeployInFlight:
			result, err := s.runDeployment(ctx, proj, env, r, versionIDs[r.manifest], candidateID, at, lastGood)
			if err != nil {
				return err
			}
			candidateID = uuid.Nil // promoted with the first deployment
			if r.kind == runDeploy {
				lastGood = result
				revisions = append(revisions, result)
			}
		case runRollback:
			if lastGood == uuid.Nil {
				return errors.New("rollback without a good revision")
			}
			if err := s.runRollback(ctx, env, r, lastGood, at); err != nil {
				return err
			}
		case runRestart:
			if err := s.runRestart(ctx, env, r, at); err != nil {
				return err
			}
		case runBackup, runRestoreFailed:
			if err := s.runBackup(ctx, proj, env, r, lastGood, at); err != nil {
				return err
			}
		}
	}

	if e.down {
		if _, err := s.st.MarkEnvironmentDown(ctx, env.ID); err != nil {
			return err
		}
		return nil
	}
	if lastGood == uuid.Nil {
		return nil // never deployed: the empty state
	}
	definition, err := s.activeDefinition(ctx, env.ID)
	if err != nil {
		return err
	}
	if err := s.seedClaims(ctx, proj, env, definition); err != nil {
		return fmt.Errorf("claims: %w", err)
	}
	if err := s.seedEnvironmentTelemetry(ctx, env.ID, definition, e.metrics); err != nil {
		return fmt.Errorf("telemetry: %w", err)
	}
	return nil
}

// runDeployment executes a real deployment (fake artifact resolver) and
// journals the rollout the reconciler would have written.
func (s *seeder) runDeployment(ctx context.Context, proj *store.Project, env *store.Environment, r seedRun,
	definitionVersionID, candidateID uuid.UUID, at time.Time, lastGood uuid.UUID) (uuid.UUID, error) {
	result, err := s.deploy.Execute(ctx, deploy.ExecuteInput{
		ProjectID:           proj.ID,
		EnvironmentID:       env.ID,
		DefinitionVersionID: definitionVersionID,
		CandidateID:         candidateID,
		Resolver:            &artifactstore.Fake{Store: artifactstore.New(s.st), ProjectID: proj.ID},
		Journal:             s.journal,
		Actor:               r.actor,
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("execute deployment: %w", err)
	}
	definition, err := s.definitionOf(ctx, definitionVersionID)
	if err != nil {
		return uuid.Nil, err
	}
	redactor, err := s.values.Redactor(ctx, env.ID, uuid.Nil)
	if err != nil {
		return uuid.Nil, err
	}
	services := serviceKeys(definition)

	// The rollout tree: one child per service, then verify and activate.
	rollout, err := s.journal.EnsureStep(ctx, result.RunID, nil, "rollout", "Roll out revision")
	if err != nil {
		return uuid.Nil, err
	}
	if err := s.journal.SetStepStatus(ctx, rollout.ID, journal.StepRunning); err != nil {
		return uuid.Nil, err
	}
	if err := s.completeStep(ctx, result.RunID, &rollout.ID, "apply:environment", "Apply environment resources", redactor,
		[]string{"created namespace", "applied 2 resources"}); err != nil {
		return uuid.Nil, err
	}
	for _, key := range services {
		lines := []string{"applied " + key + " resources"}
		if strings.HasPrefix(key, "databases.") {
			lines = []string{"claim bound to pool pg17-shared", "tenant provisioned", "credentials published"}
		} else if strings.HasPrefix(key, "buckets.") {
			lines = []string{"bucket allocated on the object store", "credentials published"}
		} else {
			lines = append(lines, "deployment "+key+" rolled to the new image")
		}
		if err := s.completeStep(ctx, result.RunID, &rollout.ID, "apply:"+key, "Apply "+key, redactor, lines); err != nil {
			return uuid.Nil, err
		}
	}

	switch r.kind {
	case runDeployInFlight:
		// Verification still running: the run stays open.
		step, err := s.journal.EnsureStep(ctx, result.RunID, &rollout.ID, "verify", "Verify health")
		if err != nil {
			return uuid.Nil, err
		}
		if err := s.journal.SetStepStatus(ctx, step.ID, journal.StepRunning); err != nil {
			return uuid.Nil, err
		}
		attempt, err := s.journal.StartAttempt(ctx, step.ID)
		if err != nil {
			return uuid.Nil, err
		}
		w := s.journal.Writer(attempt.ID, redactor)
		_ = w.Info(ctx, "waiting for 1 of 3 replicas of web to become ready")
		_ = w.Warn(ctx, "web pod web-7d9f4c-x2k1q restarted (exit 1)")
		return result.RevisionID, s.backdateRun(ctx, result.RunID, at, 0)
	case runDeployFailed:
		if err := s.failStep(ctx, result.RunID, &rollout.ID, "verify", "Verify health", redactor, []string{
			"waiting for 3 replicas of api to become ready",
			"api pod api-5b8c7d-p9q2r is crash looping: back-off restarting failed container",
			"api: readiness probe failed: HTTP 500 from /health/ready",
			"rollout deadline exceeded after 10m0s",
		}); err != nil {
			return uuid.Nil, err
		}
		if err := s.skipStep(ctx, result.RunID, &rollout.ID, "activate", "Activate revision"); err != nil {
			return uuid.Nil, err
		}
		if err := s.journal.SetStepStatus(ctx, rollout.ID, journal.StepFailed); err != nil {
			return uuid.Nil, err
		}
		if err := s.journal.FinishRun(ctx, result.RunID, journal.RunFailed); err != nil {
			return uuid.Nil, err
		}
		// The target still names the bad revision; the active pointer stays
		// on the last good one until a rollback moves the target back.
		if lastGood != uuid.Nil {
			if _, err := s.st.Pool.Exec(ctx, `UPDATE environment_targets SET active_revision_id = $2 WHERE environment_id = $1`,
				env.ID, lastGood); err != nil {
				return uuid.Nil, err
			}
		}
		return result.RevisionID, s.backdateRun(ctx, result.RunID, at, 11*time.Minute)
	}

	if err := s.completeStep(ctx, result.RunID, &rollout.ID, "verify", "Verify health", redactor,
		[]string{"all replicas ready", "routes answer 200"}); err != nil {
		return uuid.Nil, err
	}
	if err := s.completeStep(ctx, result.RunID, &rollout.ID, "activate", "Activate revision", redactor,
		[]string{"active revision set"}); err != nil {
		return uuid.Nil, err
	}
	if err := s.journal.SetStepStatus(ctx, rollout.ID, journal.StepSucceeded); err != nil {
		return uuid.Nil, err
	}
	if err := s.journal.FinishRun(ctx, result.RunID, journal.RunSucceeded); err != nil {
		return uuid.Nil, err
	}
	if _, err := s.st.SetEnvironmentActiveRevision(ctx, store.SetEnvironmentActiveRevisionParams{
		EnvironmentID: env.ID, ActiveRevisionID: &result.RevisionID,
	}); err != nil {
		return uuid.Nil, err
	}
	duration := time.Duration(90+s.rand.Intn(240)) * time.Second
	return result.RevisionID, s.backdateRun(ctx, result.RunID, at, duration)
}

func (s *seeder) runRollback(ctx context.Context, env *store.Environment, r seedRun, revisionID uuid.UUID, at time.Time) error {
	result, err := s.deploy.Rollback(ctx, deploy.RollbackInput{
		EnvironmentID: env.ID, RevisionID: revisionID, Actor: r.actor, Journal: s.journal,
	})
	if err != nil {
		return fmt.Errorf("rollback: %w", err)
	}
	if err := s.finishRollout(ctx, result.RunID, env.ID, []string{"api", "web", "worker"}); err != nil {
		return err
	}
	if _, err := s.st.SetEnvironmentActiveRevision(ctx, store.SetEnvironmentActiveRevisionParams{
		EnvironmentID: env.ID, ActiveRevisionID: &revisionID,
	}); err != nil {
		return err
	}
	return s.backdateRun(ctx, result.RunID, at, 75*time.Second)
}

func (s *seeder) runRestart(ctx context.Context, env *store.Environment, r seedRun, at time.Time) error {
	result, err := s.deploy.Restart(ctx, deploy.RestartInput{
		EnvironmentID: env.ID, ApplicationKey: r.application, Actor: r.actor, Journal: s.journal,
	})
	if err != nil {
		return fmt.Errorf("restart: %w", err)
	}
	scope := []string{r.application}
	if r.application == "" {
		scope = []string{"api", "queue"}
	}
	if err := s.finishRollout(ctx, result.RunID, env.ID, scope); err != nil {
		return err
	}
	return s.backdateRun(ctx, result.RunID, at, 40*time.Second)
}

// finishRollout journals a successful rollout into a run the deploy
// service left open for the kernel.
func (s *seeder) finishRollout(ctx context.Context, runID, envID uuid.UUID, applications []string) error {
	redactor, err := s.values.Redactor(ctx, envID, uuid.Nil)
	if err != nil {
		return err
	}
	rollout, err := s.journal.EnsureStep(ctx, runID, nil, "rollout", "Roll out revision")
	if err != nil {
		return err
	}
	if err := s.journal.SetStepStatus(ctx, rollout.ID, journal.StepRunning); err != nil {
		return err
	}
	for _, app := range applications {
		if err := s.completeStep(ctx, runID, &rollout.ID, "apply:"+app, "Apply "+app, redactor,
			[]string{"deployment " + app + " restarted"}); err != nil {
			return err
		}
	}
	if err := s.completeStep(ctx, runID, &rollout.ID, "verify", "Verify health", redactor,
		[]string{"all replicas ready"}); err != nil {
		return err
	}
	if err := s.completeStep(ctx, runID, &rollout.ID, "activate", "Activate revision", redactor,
		[]string{"active revision set"}); err != nil {
		return err
	}
	if err := s.journal.SetStepStatus(ctx, rollout.ID, journal.StepSucceeded); err != nil {
		return err
	}
	return s.journal.FinishRun(ctx, runID, journal.RunSucceeded)
}

// runBackup journals a backup (or a failed restore) run with its backup row.
func (s *seeder) runBackup(ctx context.Context, proj *store.Project, env *store.Environment, r seedRun, revisionID uuid.UUID, at time.Time) error {
	kind := backup.KindBackup
	if r.kind == runRestoreFailed {
		kind = backup.KindRestore
	}
	run, err := s.journal.CreateRun(ctx, journal.RunInput{
		Kind: kind, ProjectID: proj.ID, EnvironmentID: env.ID, Actor: r.actor,
	})
	if err != nil {
		return err
	}
	if err := s.journal.StartRun(ctx, run.ID); err != nil {
		return err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return err
	}
	row, err := s.st.CreateBackup(ctx, store.CreateBackupParams{
		ID: id, Kind: kind, EnvironmentID: env.ID,
		ProjectName: proj.Name, EnvironmentName: env.Name,
		RevisionID: utils.NilWhenZero(revisionID), RunID: &run.ID,
	})
	if err != nil {
		return err
	}
	if _, err := s.st.SetBackupStatus(ctx, store.SetBackupStatusParams{
		ID: row.ID, FromStatus: "pending", ToStatus: "running",
	}); err != nil {
		return err
	}
	redactor, err := s.values.Redactor(ctx, env.ID, uuid.Nil)
	if err != nil {
		return err
	}
	snapshot := fmt.Sprintf("%s/%s/%s", proj.Name, env.Name, at.Format("20060102T150405Z"))
	if err := s.completeStep(ctx, run.ID, nil, "target", "Check backup target", redactor,
		[]string{"backup target s3-offsite reachable"}); err != nil {
		return err
	}
	if r.kind == runRestoreFailed {
		if err := s.failStep(ctx, run.ID, nil, "restore:databases.main", "Restore databases.main", redactor, []string{
			"downloading dump " + snapshot + "/databases.main.dump",
			"pg_restore: error: could not execute query: ERROR: extension \"pg_trgm\" is not available",
			"restore aborted; the environment keeps its current data",
		}); err != nil {
			return err
		}
		if err := s.journal.FinishRun(ctx, run.ID, journal.RunFailed); err != nil {
			return err
		}
		msg := "pg_restore failed: extension pg_trgm is not available"
		if _, err := s.st.SetBackupStatus(ctx, store.SetBackupStatusParams{
			ID: row.ID, FromStatus: "running", ToStatus: "failed", Error: &msg,
		}); err != nil {
			return err
		}
		return s.backdateRun(ctx, run.ID, at, 3*time.Minute)
	}
	if err := s.completeStep(ctx, run.ID, nil, "manifest", "Write snapshot manifest", redactor,
		[]string{"snapshot " + snapshot}); err != nil {
		return err
	}
	if err := s.completeStep(ctx, run.ID, nil, "database:main", "Back up databases.main", redactor,
		[]string{"pg_dump completed (184 MiB)", "upload verified"}); err != nil {
		return err
	}
	if err := s.completeStep(ctx, run.ID, nil, "bucket:uploads", "Back up buckets.uploads", redactor,
		[]string{"synced 12,431 objects (2.1 GiB)", "upload verified"}); err != nil {
		return err
	}
	if err := s.journal.FinishRun(ctx, run.ID, journal.RunSucceeded); err != nil {
		return err
	}
	if err := s.st.SetBackupSnapshotKey(ctx, store.SetBackupSnapshotKeyParams{ID: row.ID, SnapshotKey: snapshot}); err != nil {
		return err
	}
	if _, err := s.st.SetBackupStatus(ctx, store.SetBackupStatusParams{
		ID: row.ID, FromStatus: "running", ToStatus: "succeeded",
	}); err != nil {
		return err
	}
	return s.backdateRun(ctx, run.ID, at, 6*time.Minute)
}

// --- Journal helpers ---

func (s *seeder) completeStep(ctx context.Context, runID uuid.UUID, parent *uuid.UUID, key, title string,
	redactor *redact.Redactor, lines []string) error {
	return s.closeStep(ctx, runID, parent, key, title, redactor, lines, journal.AttemptSucceeded, journal.StepSucceeded)
}

func (s *seeder) failStep(ctx context.Context, runID uuid.UUID, parent *uuid.UUID, key, title string,
	redactor *redact.Redactor, lines []string) error {
	return s.closeStep(ctx, runID, parent, key, title, redactor, lines, journal.AttemptFailed, journal.StepFailed)
}

func (s *seeder) closeStep(ctx context.Context, runID uuid.UUID, parent *uuid.UUID, key, title string,
	redactor *redact.Redactor, lines []string, attemptStatus journal.AttemptStatus, stepStatus journal.StepStatus) error {
	step, err := s.journal.EnsureStep(ctx, runID, parent, key, title)
	if err != nil {
		return err
	}
	if err := s.journal.SetStepStatus(ctx, step.ID, journal.StepRunning); err != nil {
		return err
	}
	attempt, err := s.journal.StartAttempt(ctx, step.ID)
	if err != nil {
		return err
	}
	w := s.journal.Writer(attempt.ID, redactor)
	for i, line := range lines {
		level := "info"
		if stepStatus == journal.StepFailed && i == len(lines)-1 {
			level = "error"
		} else if strings.Contains(line, "crash") || strings.Contains(line, "failed") {
			level = "warn"
		}
		if err := w.Log(ctx, level, line, nil); err != nil {
			return err
		}
	}
	if err := s.journal.FinishAttempt(ctx, attempt.ID, attemptStatus); err != nil {
		return err
	}
	return s.journal.SetStepStatus(ctx, step.ID, stepStatus)
}

func (s *seeder) skipStep(ctx context.Context, runID uuid.UUID, parent *uuid.UUID, key, title string) error {
	step, err := s.journal.EnsureStep(ctx, runID, parent, key, title)
	if err != nil {
		return err
	}
	return s.journal.SetStepStatus(ctx, step.ID, journal.StepSkipped)
}

// backdateRun shifts a run and everything under it so it started at `at`
// and lasted `duration` (0 keeps it running until now). The journal writes
// wall-clock timestamps; the history only reads right once they are moved.
func (s *seeder) backdateRun(ctx context.Context, runID uuid.UUID, at time.Time, duration time.Duration) error {
	run, err := s.st.GetRunByID(ctx, runID)
	if err != nil {
		return err
	}
	// Compress the real elapsed time (milliseconds) into the wanted
	// duration by shifting every timestamp back to `at` and then scaling
	// offsets: simpler and good enough is a shift plus a stretched finish.
	shift := run.CreatedAt.Sub(at)
	statements := []string{
		`UPDATE run_logs SET ts = ts - $2::interval WHERE attempt_id IN (SELECT a.id FROM attempts a JOIN steps s ON s.id = a.step_id WHERE s.run_id = $1)`,
		`UPDATE attempts SET started_at = started_at - $2::interval, finished_at = finished_at - $2::interval WHERE step_id IN (SELECT id FROM steps WHERE run_id = $1)`,
		`UPDATE steps SET created_at = created_at - $2::interval, started_at = started_at - $2::interval, finished_at = finished_at - $2::interval WHERE run_id = $1`,
		`UPDATE runs SET created_at = created_at - $2::interval, started_at = started_at - $2::interval, finished_at = finished_at - $2::interval WHERE id = $1`,
		`UPDATE backups SET created_at = created_at - $2::interval, finished_at = finished_at - $2::interval WHERE run_id = $1`,
		`UPDATE revisions SET created_at = created_at - $2::interval WHERE id IN (SELECT revision_id FROM deployments WHERE run_id = $1) AND created_at > $3`,
	}
	for _, stmt := range statements {
		args := []any{runID, shift}
		if strings.Contains(stmt, "$3") {
			args = append(args, at)
		}
		if _, err := s.st.Pool.Exec(ctx, stmt, args...); err != nil {
			return fmt.Errorf("backdate run: %w", err)
		}
	}
	if duration > 0 {
		// Spread the leaf steps evenly over the wanted duration, let parents
		// span their children, and keep skipped steps unstarted; attempts
		// follow their steps and log lines spread within their attempt.
		leaves := `
			WITH ordered AS (
				SELECT id, row_number() OVER (ORDER BY created_at, key) AS n, count(*) OVER () AS total
				FROM steps s WHERE run_id = $1 AND NOT EXISTS (SELECT 1 FROM steps c WHERE c.parent_id = s.id)
			)
			UPDATE steps s SET
				started_at  = $2::timestamptz + ($3::interval * ((o.n - 1)::double precision / o.total)),
				finished_at = CASE WHEN s.finished_at IS NULL THEN NULL
				              ELSE $2::timestamptz + ($3::interval * (o.n::double precision / o.total)) END
			FROM ordered o WHERE o.id = s.id`
		if _, err := s.st.Pool.Exec(ctx, leaves, runID, at, duration); err != nil {
			return fmt.Errorf("spread steps: %w", err)
		}
		statements := []string{
			`UPDATE steps p SET
				started_at  = (SELECT min(c.started_at) FROM steps c WHERE c.parent_id = p.id),
				finished_at = CASE WHEN p.finished_at IS NULL THEN NULL
				              ELSE (SELECT max(c.finished_at) FROM steps c WHERE c.parent_id = p.id) END
			 WHERE p.run_id = $1 AND EXISTS (SELECT 1 FROM steps c WHERE c.parent_id = p.id)`,
			`UPDATE steps SET started_at = NULL WHERE run_id = $1 AND status IN ('skipped', 'pending', 'cancelled')`,
			`UPDATE attempts a SET started_at = s.started_at, finished_at = s.finished_at
			 FROM steps s WHERE s.id = a.step_id AND s.run_id = $1`,
			`UPDATE run_logs l SET ts = a.started_at + (coalesce(a.finished_at, now()) - a.started_at) * (l.seq::double precision / greatest(m.max_seq, 1))
			 FROM attempts a JOIN steps s ON s.id = a.step_id
			 JOIN (SELECT attempt_id, max(seq) AS max_seq FROM run_logs GROUP BY attempt_id) m ON m.attempt_id = a.id
			 WHERE l.attempt_id = a.id AND s.run_id = $1`,
		}
		for _, stmt := range statements {
			if _, err := s.st.Pool.Exec(ctx, stmt, runID); err != nil {
				return fmt.Errorf("spread steps: %w", err)
			}
		}
		finish := []string{
			`UPDATE runs SET finished_at = $2::timestamptz + $3::interval WHERE id = $1 AND finished_at IS NOT NULL`,
			`UPDATE backups SET finished_at = $2::timestamptz + $3::interval WHERE run_id = $1 AND finished_at IS NOT NULL`,
		}
		for _, stmt := range finish {
			if _, err := s.st.Pool.Exec(ctx, stmt, runID, at, duration); err != nil {
				return fmt.Errorf("finish run: %w", err)
			}
		}
	}
	return nil
}

// backdateProject moves the project, its environments, and its definition
// versions to the start of the seeded span, so "created" dates precede the
// history instead of reading as today.
func (s *seeder) backdateProject(ctx context.Context, projectID uuid.UUID, at time.Time) error {
	statements := []string{
		`UPDATE projects SET created_at = $2, updated_at = $2 WHERE id = $1`,
		`UPDATE environments SET created_at = $2, updated_at = $2 WHERE project_id = $1`,
		`UPDATE definition_versions SET created_at = $2 WHERE project_id = $1`,
		`UPDATE project_members SET created_at = $2, updated_at = $2 WHERE project_id = $1`,
		`UPDATE environment_access SET created_at = $2, updated_at = $2 WHERE project_id = $1`,
	}
	for _, stmt := range statements {
		if _, err := s.st.Pool.Exec(ctx, stmt, projectID, at); err != nil {
			return fmt.Errorf("backdate project: %w", err)
		}
	}
	return nil
}

// --- Definitions ---

func (s *seeder) definitionOf(ctx context.Context, definitionVersionID uuid.UUID) (compiler.ProjectDefinition, error) {
	row, err := s.st.GetDefinitionVersionByID(ctx, definitionVersionID)
	if err != nil {
		return compiler.ProjectDefinition{}, err
	}
	return compiler.DecodeDefinition(row.Definition)
}

func (s *seeder) activeDefinition(ctx context.Context, environmentID uuid.UUID) (compiler.ProjectDefinition, error) {
	id, ok, err := s.deploy.ActiveDefinitionVersion(ctx, environmentID)
	if err != nil {
		return compiler.ProjectDefinition{}, err
	}
	if !ok {
		return compiler.ProjectDefinition{}, errors.New("environment has no active definition")
	}
	return s.definitionOf(ctx, id)
}

// serviceKeys lists a definition's services in apply order: databases and
// buckets first (applications wait on them), then applications.
func serviceKeys(definition compiler.ProjectDefinition) []string {
	var keys []string
	for _, key := range utils.SortedKeys(definition.Databases) {
		keys = append(keys, "databases."+key)
	}
	for _, key := range utils.SortedKeys(definition.Buckets) {
		keys = append(keys, "buckets."+key)
	}
	keys = append(keys, utils.SortedKeys(definition.Applications)...)
	return keys
}

// --- Substrate claims ---

// seedClaims records provisioned database and bucket claims for the
// environment's active definition so the service pages show connection
// details instead of "provisioning".
func (s *seeder) seedClaims(ctx context.Context, proj *store.Project, env *store.Environment, definition compiler.ProjectDefinition) error {
	if len(definition.Databases) > 0 {
		cluster, err := s.db.LiveSharedCluster(ctx, "postgres", 17)
		if errors.Is(err, dbstore.ErrNotFound) {
			cluster, err = s.db.CreateCluster(ctx, dbstore.ClusterInput{
				Name: "pg17-shared", Engine: "postgres", Major: 17, Class: "shared",
				Instances: 2, StorageBytes: 100 * gib, Image: "ghcr.io/cloudnative-pg/postgresql:17.5",
			})
		}
		if err != nil {
			return err
		}
		for _, key := range utils.SortedKeys(definition.Databases) {
			db := definition.Databases[key]
			owner := dbstore.ServiceOwner(proj.ID, env.ID, proj.Name, env.Name, key)
			row, err := s.db.EnsureClaim(ctx, owner, dbstore.ClaimSpec{
				Engine: "postgres", Major: 17, Isolation: "shared", Availability: "single",
				StorageBytes: db.StorageBytes, Extensions: db.Extensions,
			})
			if err != nil {
				return fmt.Errorf("database claim %s: %w", key, err)
			}
			if _, err := s.db.BindClaim(ctx, row.ID, cluster.ID); err != nil {
				return err
			}
			slug := fmt.Sprintf("%s_%s_%s", proj.Name, env.Name, key)
			if _, err := s.db.RecordTenant(ctx, dbstore.TenantInput{
				ClaimID: row.ID, ClusterID: cluster.ID,
				DatabaseName:     slug,
				RoleName:         slug,
				CredentialSecret: "db-" + strings.ReplaceAll(slug, "_", "-"),
				Host:             cluster.Name + "-rw.skali-platform.svc",
				Port:             5432,
			}); err != nil {
				return err
			}
			if _, err := s.db.TransitionClaim(ctx, row.ID, claim.PhaseProvisioned); err != nil {
				return err
			}
		}
	}
	if len(definition.Buckets) > 0 {
		objectStore, err := s.db.LiveObjectStore(ctx)
		if errors.Is(err, dbstore.ErrNotFound) {
			objectStore, err = s.db.CreateObjectStore(ctx, dbstore.StoreInput{
				Name: "seaweed", Masters: 1, VolumeServers: 3, Replication: "001",
				VolumeStorageBytes: 200 * gib, Image: "chrislusf/seaweedfs:3.80",
			})
		}
		if err != nil {
			return err
		}
		for _, key := range utils.SortedKeys(definition.Buckets) {
			bucket := definition.Buckets[key]
			owner := dbstore.ServiceOwner(proj.ID, env.ID, proj.Name, env.Name, key)
			row, err := s.db.EnsureBucketClaim(ctx, owner, dbstore.BucketSpec{
				Visibility: "private", Versioning: "disabled",
				StorageQuotaBytes: bucket.StorageQuotaBytes,
			})
			if err != nil {
				return fmt.Errorf("bucket claim %s: %w", key, err)
			}
			id := row.ID.String()
			name := fmt.Sprintf("%s-%s-%s-%s", proj.Name, env.Name, key, id[len(id)-6:])
			if _, err := s.db.RecordAllocation(ctx, dbstore.AllocationInput{
				ClaimID: row.ID, StoreID: objectStore.ID, BucketName: name,
				AccessKeyID:      "AK" + strings.ToUpper(id[len(id)-12:]),
				CredentialSecret: "bucket-" + name,
				Endpoint:         "https://s3.example.com",
				Region:           "us-east-1",
			}); err != nil {
				return err
			}
			if _, err := s.db.TransitionBucketClaim(ctx, row.ID, claim.PhaseProvisioned); err != nil {
				return err
			}
		}
	}
	return nil
}

// --- Telemetry ---

// ticks yields sample times over the span: dense for the last hours, sparse
// further back, matching what each console window needs.
func (s *seeder) ticks() []time.Time {
	var out []time.Time
	for t := s.now.Add(-s.span); !t.After(s.now); {
		out = append(out, t)
		age := s.now.Sub(t)
		switch {
		case age > 26*time.Hour:
			t = t.Add(30 * time.Minute)
		case age > 2*time.Hour:
			t = t.Add(5 * time.Minute)
		default:
			t = t.Add(time.Minute)
		}
	}
	return out
}

// load shapes a daily curve (busier in the afternoon) with a slow ramp and
// noise; jitter scales the noise.
func (s *seeder) load(base int64, at time.Time, jitter float64) int64 {
	hour := float64(at.Hour()) + float64(at.Minute())/60
	daily := 0.75 + 0.35*math.Sin((hour-6)/24*2*math.Pi)
	ramp := 0.9 + 0.2*(1-s.now.Sub(at).Hours()/s.span.Hours())
	noise := 1 + jitter*(s.rand.Float64()*2-1)
	return int64(float64(base) * daily * ramp * noise)
}

func (s *seeder) seedEnvironmentTelemetry(ctx context.Context, envID uuid.UUID, definition compiler.ProjectDefinition, loads map[string]appLoad) error {
	apps := utils.SortedKeys(definition.Applications)
	if len(apps) == 0 {
		return nil
	}
	for _, at := range s.ticks() {
		appParams := store.InsertAppMetricSamplesParams{SampledAt: at}
		edgeParams := store.InsertEdgeMetricSamplesParams{SampledAt: at}
		for _, key := range apps {
			l, ok := loads[key]
			if !ok {
				l = appLoad{cpuMillicores: 50, memoryBytes: 128 * mib, replicas: 1}
			}
			appParams.EnvironmentIds = append(appParams.EnvironmentIds, envID)
			appParams.ApplicationKeys = append(appParams.ApplicationKeys, key)
			appParams.CpuMillicores = append(appParams.CpuMillicores, s.load(l.cpuMillicores, at, 0.25))
			appParams.MemoryBytes = append(appParams.MemoryBytes, s.load(l.memoryBytes, at, 0.05))
			appParams.PodCounts = append(appParams.PodCounts, l.replicas)
			if l.requestsPerMinute == 0 {
				continue
			}
			for _, route := range utils.SortedKeys(definition.Applications[key].Routes) {
				requests := s.load(l.requestsPerMinute, at, 0.4)
				edgeParams.EnvironmentIds = append(edgeParams.EnvironmentIds, envID)
				edgeParams.ApplicationKeys = append(edgeParams.ApplicationKeys, key)
				edgeParams.RouteKeys = append(edgeParams.RouteKeys, route)
				edgeParams.Requests = append(edgeParams.Requests, requests)
				edgeParams.RequestBytes = append(edgeParams.RequestBytes, requests*int64(600+s.rand.Intn(900)))
				edgeParams.ResponseBytes = append(edgeParams.ResponseBytes, requests*int64(4000+s.rand.Intn(20000)))
			}
		}
		if _, err := s.st.InsertAppMetricSamples(ctx, appParams); err != nil {
			return fmt.Errorf("app samples: %w", err)
		}
		if len(edgeParams.EnvironmentIds) > 0 {
			if _, err := s.st.InsertEdgeMetricSamples(ctx, edgeParams); err != nil {
				return fmt.Errorf("edge samples: %w", err)
			}
		}
	}

	// Storage footprints: the newest sample per service is what the
	// console reads; a few hours of history keeps the cutoff comfortable.
	for i := 0; i < 6; i++ {
		at := s.now.Add(-time.Duration(i) * time.Hour)
		params := store.InsertStorageSamplesParams{SampledAt: at}
		add := func(key, kind string, used int64, measured bool, capacity int64) {
			params.EnvironmentIds = append(params.EnvironmentIds, envID)
			params.ServiceKeys = append(params.ServiceKeys, key)
			params.Kinds = append(params.Kinds, kind)
			params.UsedBytes = append(params.UsedBytes, used)
			params.UsedMeasured = append(params.UsedMeasured, measured)
			params.CapacityBytes = append(params.CapacityBytes, capacity)
		}
		for _, key := range apps {
			app := definition.Applications[key]
			l := loads[key]
			var volumeCapacity int64
			for _, volume := range app.Volumes {
				volumeCapacity += volume.SizeBytes
			}
			if volumeCapacity > 0 {
				add(key, "volume", volumeCapacity*int64(35+s.rand.Intn(40))/100, true, volumeCapacity)
			}
			if limit := app.Resources.Limits.TemporaryStorageBytes; limit > 0 {
				add(key, "temporary", int64(120+s.rand.Intn(300))*mib*max(l.replicas, 1), true, limit*max(l.replicas, 1))
			}
		}
		for _, key := range utils.SortedKeys(definition.Databases) {
			capacity := definition.Databases[key].StorageBytes
			add("databases."+key, "database", int64(180+s.rand.Intn(900))*mib, true, capacity)
		}
		for _, key := range utils.SortedKeys(definition.Buckets) {
			capacity := definition.Buckets[key].StorageQuotaBytes
			add("buckets."+key, "bucket", capacity*int64(10+s.rand.Intn(60))/100, true, capacity)
		}
		if len(params.EnvironmentIds) == 0 {
			return nil
		}
		if _, err := s.st.InsertStorageSamples(ctx, params); err != nil {
			return fmt.Errorf("storage samples: %w", err)
		}
	}
	return nil
}

func (s *seeder) seedNodeTelemetry(ctx context.Context) error {
	for _, at := range s.ticks() {
		params := store.InsertNodeMetricSamplesParams{SampledAt: at}
		for _, n := range seedNodes {
			params.NodeNames = append(params.NodeNames, n.name)
			params.CpuMillicores = append(params.CpuMillicores, s.load(n.cpuBase, at, 0.2))
			params.MemoryBytes = append(params.MemoryBytes, s.load(n.memBase, at, 0.04))
			params.CpuAllocatableMillicores = append(params.CpuAllocatableMillicores, n.cpuAllocat)
			params.MemoryAllocatableBytes = append(params.MemoryAllocatableBytes, n.memAllocat)
		}
		if _, err := s.st.InsertNodeMetricSamples(ctx, params); err != nil {
			return err
		}
	}
	for i := 0; i < 3; i++ {
		at := s.now.Add(-time.Duration(i) * 20 * time.Minute)
		params := store.InsertStorageNodeSamplesParams{SampledAt: at}
		for _, n := range seedNodes {
			volumes := n.diskCapacity * 8 / 100
			databases := n.diskCapacity * 6 / 100
			objects := n.diskCapacity * 11 / 100
			used := volumes + databases + objects + n.diskImages + n.diskTemporary + n.diskCapacity*3/100
			params.NodeNames = append(params.NodeNames, n.name)
			params.CapacityBytes = append(params.CapacityBytes, n.diskCapacity)
			params.UsedBytes = append(params.UsedBytes, used)
			params.AvailableBytes = append(params.AvailableBytes, n.diskCapacity-used)
			params.VolumesBytes = append(params.VolumesBytes, volumes)
			params.DatabasesBytes = append(params.DatabasesBytes, databases)
			params.ObjectsBytes = append(params.ObjectsBytes, objects)
			params.ImagesBytes = append(params.ImagesBytes, n.diskImages)
			params.TemporaryBytes = append(params.TemporaryBytes, n.diskTemporary)
		}
		if _, err := s.st.InsertStorageNodeSamples(ctx, params); err != nil {
			return err
		}
	}
	return nil
}

// --- Reset ---

// reset removes what a previous seed wrote: the seeded projects (cascading
// to environments, runs, revisions, telemetry, and backups), their claims
// and tenants, the seeded users, and the seeded node telemetry. Anything
// else in the database is left alone.
func (s *seeder) reset(ctx context.Context) error {
	for _, p := range seedProjects {
		proj, err := s.st.GetProjectByName(ctx, p.name)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		statements := []string{
			// Hostname claims restrict environment deletion on purpose (a
			// purge releases them first); the reset releases them the same way.
			`DELETE FROM hostname_claims WHERE environment_id IN (SELECT id FROM environments WHERE project_id = $1)`,
			`DELETE FROM database_tenants WHERE claim_id IN (SELECT id FROM database_claims WHERE project_id = $1)`,
			`DELETE FROM database_placements WHERE claim_id IN (SELECT id FROM database_claims WHERE project_id = $1)`,
			`DELETE FROM database_claims WHERE project_id = $1`,
			`DELETE FROM bucket_allocations WHERE claim_id IN (SELECT id FROM bucket_claims WHERE project_id = $1)`,
			`DELETE FROM bucket_claims WHERE project_id = $1`,
			`DELETE FROM artifacts WHERE project_id = $1`,
			`DELETE FROM projects WHERE id = $1`,
		}
		for _, stmt := range statements {
			if _, err := s.st.Pool.Exec(ctx, stmt, proj.ID); err != nil {
				return fmt.Errorf("reset project %s: %w", p.name, err)
			}
		}
	}
	for _, u := range seedUsers {
		if _, err := s.st.DeleteUserByEmail(ctx, strings.ToLower(u.email)); err != nil {
			return fmt.Errorf("reset user %s: %w", u.email, err)
		}
	}
	for _, n := range seedNodes {
		for _, table := range []string{"metric_node_samples", "metric_storage_node_samples"} {
			if _, err := s.st.Pool.Exec(ctx, `DELETE FROM `+table+` WHERE node_name = $1`, n.name); err != nil {
				return err
			}
		}
	}
	// The shared pool and object store are only seed rows when nothing
	// else references them any more.
	if _, err := s.st.Pool.Exec(ctx, `DELETE FROM database_clusters WHERE name = 'pg17-shared' AND NOT EXISTS (SELECT 1 FROM database_placements WHERE cluster_id = database_clusters.id)`); err != nil {
		return err
	}
	if _, err := s.st.Pool.Exec(ctx, `DELETE FROM object_stores WHERE name = 'seaweed' AND NOT EXISTS (SELECT 1 FROM bucket_allocations WHERE store_id = object_stores.id)`); err != nil {
		return err
	}
	fmt.Println("removed previously seeded data")
	return nil
}
