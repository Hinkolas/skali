// Package sqlsession runs the SQL sessions behind `skali sql`: one
// short-lived database client per session, connected to one database
// service with that database's own login role. The client is a Job in the
// environment's namespace, the namespace the pool's access policy already
// admits for the environment's applications, consuming the connection
// Secret the substrate mirrors there for them. It holds nothing but those
// credentials (no service account token, no other Secret), so even psql's
// shell escape reaches no more than the database itself. The session
// streams through the exec subresource exactly like podexec; the Job is
// deleted when the session ends and reaps itself (active deadline plus TTL)
// when the daemon goes away first.
//
// The database's own server pod is never a target: several environments'
// databases share a pool, and a shell there would reach all of them.
package sqlsession

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8s "k8s.io/client-go/kubernetes"

	"github.com/Hinkolas/skali/internal/claim"
	"github.com/Hinkolas/skali/internal/dbstore"
	"github.com/Hinkolas/skali/internal/kube"
	skalikube "github.com/Hinkolas/skali/internal/kubernetes"
	"github.com/Hinkolas/skali/internal/layout"
	"github.com/Hinkolas/skali/internal/naming"
	"github.com/Hinkolas/skali/internal/podexec"
	"github.com/Hinkolas/skali/internal/store"
)

var (
	// ErrNoCluster: skalid runs API-only; there is nothing to start.
	ErrNoCluster = errors.New("sqlsession: no cluster connected")
	// ErrEnvironmentNotFound mirrors the store lookup.
	ErrEnvironmentNotFound = errors.New("sqlsession: environment not found")
	// ErrDatabaseNotFound: the environment holds no live claim for the
	// requested database key.
	ErrDatabaseNotFound = errors.New("sqlsession: database not found")
	// ErrNotProvisioned: the claim exists but has no live tenant yet.
	ErrNotProvisioned = errors.New("sqlsession: the database is not provisioned yet")
	// ErrInvalidOptions: the request itself is malformed.
	ErrInvalidOptions = errors.New("sqlsession: invalid options")
)

// Format selects the client and how batch results render.
type Format string

const (
	// FormatPsql is psql's own aligned output, and the only format of an
	// interactive (TTY) session. psql meta-commands work.
	FormatPsql Format = "psql"
	// FormatCSV is psql --csv without command tags.
	FormatCSV Format = "csv"
	// FormatJSON is the sql-runner's document: typed columns, rows as
	// arrays, the command tag, and the error.
	FormatJSON Format = "json"
)

// ParseFormat validates a requested format; empty means psql.
func ParseFormat(raw string) (Format, error) {
	switch Format(raw) {
	case "", FormatPsql:
		return FormatPsql, nil
	case FormatCSV, FormatJSON:
		return Format(raw), nil
	}
	return "", fmt.Errorf("%w: format %q (want psql, csv, or json)", ErrInvalidOptions, raw)
}

// Session lifetime and startup bounds.
const (
	// MaxLifetime caps one session: the Job's active deadline. A daemon
	// that dies mid-session leaves the client running at most this long.
	MaxLifetime = 8 * time.Hour
	// MaxStatementTimeout bounds the statement_timeout a client may ask
	// for; longer work belongs in an application command.
	MaxStatementTimeout = 24 * time.Hour
	// startTimeout bounds how long Start waits for the client pod (image
	// pull included).
	startTimeout = 3 * time.Minute
	pollInterval = 250 * time.Millisecond
	// finishedTTL reaps a Job the daemon could not delete itself.
	finishedTTL = int32(60)

	// LabelSession marks every client Job and pod. They carry neither the
	// managed nor the service label, so observation, status, exec, and
	// pruning never see them.
	LabelSession = "skali.dev/sql-session"
	// containerName is the client container's name.
	containerName = "sql"
	// clientUID runs the client as nobody; neither image needs more.
	clientUID = int64(65534)
)

// Options is one client's requested session.
type Options struct {
	Database string // the database service key, databases.<key>
	Format   Format
	TTY      bool
	// ReadOnly sets default_transaction_read_only: a guard against
	// accidental writes, not a permission (the session's role may lift
	// it with SET).
	ReadOnly bool
	// StatementTimeout sets statement_timeout; zero leaves the server's.
	StatementTimeout time.Duration
}

// Target is a fully validated session, resolved before anything is
// started so every refusal is an ordinary error response.
type Target struct {
	EnvironmentID uuid.UUID
	Namespace     string
	Database      string
	// PostgresImage is the pool's pinned image, whose psql matches the
	// server's major version.
	PostgresImage string
	Format        Format
	TTY           bool
	ReadOnly      bool
	// StatementTimeout is the requested statement_timeout.
	StatementTimeout time.Duration
}

// Service resolves and runs SQL sessions. Kube is nil in API-only mode.
type Service struct {
	Kube      *kube.Client
	Store     *store.Store
	Databases *dbstore.Service
	// ManagedCluster pins clients to application nodes, like the
	// environment's own pods.
	ManagedCluster bool
	// RunnerImage resolves the skalid image the JSON format runs in.
	RunnerImage func(ctx context.Context) (string, error)
}

// Resolve validates the request and the database it names. It reads the
// store only; no cluster call happens before Run.
func (s *Service) Resolve(ctx context.Context, environmentID uuid.UUID, opts Options) (*Target, error) {
	if s == nil || s.Kube == nil {
		return nil, ErrNoCluster
	}
	if err := naming.CheckKey(opts.Database); err != nil {
		return nil, fmt.Errorf("%w: invalid database: %v", ErrInvalidOptions, err)
	}
	format, err := ParseFormat(string(opts.Format))
	if err != nil {
		return nil, err
	}
	if opts.TTY && format != FormatPsql {
		return nil, fmt.Errorf("%w: an interactive session always uses the psql format", ErrInvalidOptions)
	}
	if opts.StatementTimeout < 0 || opts.StatementTimeout > MaxStatementTimeout {
		return nil, fmt.Errorf("%w: statement timeout must be between 0 and %s", ErrInvalidOptions, MaxStatementTimeout)
	}

	env, err := s.Store.GetEnvironmentByID(ctx, environmentID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrEnvironmentNotFound
		}
		return nil, fmt.Errorf("sqlsession: get environment: %w", err)
	}
	row, err := s.Databases.LiveServiceClaim(ctx, environmentID, opts.Database)
	if err != nil {
		if errors.Is(err, dbstore.ErrNotFound) {
			return nil, ErrDatabaseNotFound
		}
		return nil, fmt.Errorf("sqlsession: load the claim: %w", err)
	}
	if claim.Phase(row.Phase) != claim.PhaseProvisioned {
		return nil, ErrNotProvisioned
	}
	tenant, err := s.Databases.LiveTenant(ctx, row.ID)
	if err != nil {
		if errors.Is(err, dbstore.ErrNotFound) {
			return nil, ErrNotProvisioned
		}
		return nil, fmt.Errorf("sqlsession: load the tenant: %w", err)
	}
	cluster, err := s.Databases.GetCluster(ctx, tenant.ClusterID)
	if err != nil {
		return nil, fmt.Errorf("sqlsession: load the pool: %w", err)
	}
	return &Target{
		EnvironmentID:    environmentID,
		Namespace:        skalikube.NamespaceName(env.ID.String()),
		Database:         opts.Database,
		PostgresImage:    cluster.Image,
		Format:           format,
		TTY:              opts.TTY,
		ReadOnly:         opts.ReadOnly,
		StatementTimeout: opts.StatementTimeout,
	}, nil
}

// StartError reports that the client never came up, as opposed to the
// session itself failing; the API surfaces it with its own code.
type StartError struct {
	Err error
}

func (e *StartError) Error() string { return "start the database client: " + e.Err.Error() }
func (e *StartError) Unwrap() error { return e.Err }

// Run starts the client, streams the session over streams until the client
// process ends or ctx is cancelled, and deletes the client. ready is called
// once the client runs, right before its process starts reading stdin. It
// reports the pod the session ran in (empty when it never started). A
// nonzero exit surfaces as *exec.CodeExitError like podexec; a client that
// never started as *StartError.
func (s *Service) Run(ctx context.Context, target *Target, streams podexec.Streams, ready func()) (string, error) {
	image := target.PostgresImage
	if target.Format == FormatJSON {
		if s.RunnerImage == nil {
			return "", &StartError{Err: errors.New("the json format is unavailable: no runner image")}
		}
		resolved, err := s.RunnerImage(ctx)
		if err != nil {
			return "", &StartError{Err: err}
		}
		image = resolved
	}
	name, err := sessionName()
	if err != nil {
		return "", &StartError{Err: err}
	}
	job := RenderJob(name, target, image, s.ManagedCluster)

	clientset := s.Kube.Clientset
	defer deleteJob(context.WithoutCancel(ctx), clientset, target.Namespace, name)
	pod, err := start(ctx, clientset, job)
	if err != nil {
		return pod, err
	}
	ready()
	return pod, s.Kube.ExecStream(ctx, target.Namespace, pod, containerName, Command(target), kube.ExecStreamOptions{
		Stdin:  streams.Stdin,
		Stdout: streams.Stdout,
		Stderr: streams.Stderr,
		TTY:    target.TTY,
		Resize: streams.Resize,
	})
}

func sessionName() (string, error) {
	var suffix [6]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", fmt.Errorf("session name: %w", err)
	}
	return "skali-sql-" + hex.EncodeToString(suffix[:]), nil
}

// readyWait gives a fresh pod's first connection the same grace backup Jobs
// give theirs: until the CNI's network policy enforcement registers the
// pod's address the pool refuses it, so the client waits for pg_isready
// (at most 30 seconds, then the client reports whatever is wrong itself).
const readyWait = `for i in $(seq 1 30); do pg_isready -q && break; sleep 1; done; exec "$@"`

// Command is what one session runs in the client container. The script of
// a batch session arrives on stdin, never in the command line, so SQL text
// does not show up in process listings or the exec audit log. The JSON
// runner retries its own connection, so it needs no wait.
func Command(target *Target) []string {
	if target.Format == FormatJSON {
		return []string{"skalid", "sql-runner"}
	}
	psql := []string{"psql", "--no-psqlrc"}
	if !target.TTY {
		// ON_ERROR_STOP makes a failing statement end the script with
		// status 3 instead of carrying on; reading the script as a file
		// prefixes errors with their line.
		psql = append(psql, "--set", "ON_ERROR_STOP=1", "--pset", "pager=off")
		if target.Format == FormatCSV {
			psql = append(psql, "--csv", "--quiet")
		}
		psql = append(psql, "--file", "-")
	}
	return append([]string{"/bin/sh", "-c", readyWait, "sh"}, psql...)
}

// pgOptions renders the session settings as PGOPTIONS, which both psql
// and the runner hand to the server at connect time.
func pgOptions(target *Target) string {
	var options []string
	if target.ReadOnly {
		options = append(options, "-c default_transaction_read_only=on")
	}
	if target.StatementTimeout > 0 {
		options = append(options, "-c statement_timeout="+strconv.FormatInt(target.StatementTimeout.Milliseconds(), 10))
	}
	return strings.Join(options, " ")
}

// RenderJob builds the client Job: one container that idles until the
// session execs into it, with the database's connection Secret as the
// standard PG* environment.
func RenderJob(name string, target *Target, image string, managedCluster bool) *batchv1.Job {
	secret := skalikube.OutputSecretName("databases", target.Database)
	fromSecret := func(variable, key string) corev1.EnvVar {
		return corev1.EnvVar{Name: variable, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: secret}, Key: key}}}
	}
	env := []corev1.EnvVar{
		fromSecret("PGHOST", "host"),
		fromSecret("PGPORT", "port"),
		fromSecret("PGDATABASE", "name"),
		fromSecret("PGUSER", "username"),
		fromSecret("PGPASSWORD", "password"),
		{Name: "PGAPPNAME", Value: "skali-sql"},
		{Name: "PGCONNECT_TIMEOUT", Value: "10"},
		// nobody has no home; psql keeps its history there.
		{Name: "HOME", Value: "/tmp"},
	}
	if options := pgOptions(target); options != "" {
		env = append(env, corev1.EnvVar{Name: "PGOPTIONS", Value: options})
	}

	labels := map[string]string{LabelSession: name}
	automount := false
	serviceLinks := false
	grace := int64(1)
	backoff := int32(0)
	deadline := int64(MaxLifetime / time.Second)
	ttl := finishedTTL
	spec := corev1.PodSpec{
		RestartPolicy:                 corev1.RestartPolicyNever,
		AutomountServiceAccountToken:  &automount,
		EnableServiceLinks:            &serviceLinks,
		TerminationGracePeriodSeconds: &grace,
		SecurityContext: &corev1.PodSecurityContext{
			RunAsNonRoot:   new(true),
			RunAsUser:      new(clientUID),
			RunAsGroup:     new(clientUID),
			SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
		},
		Containers: []corev1.Container{{
			Name:  containerName,
			Image: image,
			// The idle process outlives the session by nothing: the Job's
			// deadline ends it at the same instant.
			Command: []string{"sleep", strconv.FormatInt(deadline, 10)},
			Env:     env,
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("10m"),
					corev1.ResourceMemory: resource.MustParse("32Mi"),
				},
				Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("256Mi")},
			},
			SecurityContext: &corev1.SecurityContext{
				AllowPrivilegeEscalation: new(false),
				Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
			},
		}},
	}
	if managedCluster {
		spec.NodeSelector = map[string]string{
			layout.CapabilityLabel(layout.CapabilityApplication): layout.CapabilityLabelValue,
		}
	}
	return &batchv1.Job{
		TypeMeta: metav1.TypeMeta{APIVersion: "batch/v1", Kind: "Job"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: target.Namespace,
			Labels:    labels,
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoff,
			ActiveDeadlineSeconds:   &deadline,
			TTLSecondsAfterFinished: &ttl,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec:       spec,
			},
		},
	}
}

// start creates the Job and waits until its pod runs, failing fast on
// states that never resolve on their own (a missing Secret, an image that
// cannot be pulled).
func start(ctx context.Context, clientset k8s.Interface, job *batchv1.Job) (string, error) {
	if _, err := clientset.BatchV1().Jobs(job.Namespace).Create(ctx, job, metav1.CreateOptions{}); err != nil {
		return "", &StartError{Err: fmt.Errorf("create the client job: %w", err)}
	}
	waitCtx, cancel := context.WithTimeout(ctx, startTimeout)
	defer cancel()
	selector := LabelSession + "=" + job.Name
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	last := "waiting for the client pod to be created"
	for {
		pods, err := clientset.CoreV1().Pods(job.Namespace).List(waitCtx, metav1.ListOptions{LabelSelector: selector})
		if err != nil && waitCtx.Err() == nil {
			last = "list the client pod: " + err.Error()
		}
		if err == nil {
			for i := range pods.Items {
				pod := &pods.Items[i]
				ready, state, failed := podState(pod)
				if ready {
					return pod.Name, nil
				}
				if failed {
					return pod.Name, &StartError{Err: errors.New(state)}
				}
				last = state
			}
		}
		select {
		case <-waitCtx.Done():
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			return "", &StartError{Err: fmt.Errorf("the client did not start within %s: %s", startTimeout, last)}
		case <-ticker.C:
		}
	}
}

// podState reads one client pod: whether the client container runs, a
// description of where it stands, and whether it can no longer get there.
func podState(pod *corev1.Pod) (ready bool, state string, failed bool) {
	switch pod.Status.Phase {
	case corev1.PodSucceeded, corev1.PodFailed:
		return false, "the client pod ended before the session started: " + describePhase(pod), true
	}
	for _, status := range pod.Status.ContainerStatuses {
		if status.Name != containerName {
			continue
		}
		if status.State.Running != nil {
			return true, "", false
		}
		if waiting := status.State.Waiting; waiting != nil && waiting.Reason != "" {
			description := waiting.Reason
			if waiting.Message != "" {
				description += ": " + waiting.Message
			}
			switch waiting.Reason {
			case "ErrImagePull", "ImagePullBackOff", "InvalidImageName",
				"CreateContainerConfigError", "CreateContainerError":
				return false, description, true
			}
			return false, description, false
		}
	}
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodScheduled && condition.Status == corev1.ConditionFalse && condition.Message != "" {
			return false, "not scheduled: " + condition.Message, false
		}
	}
	return false, "the client pod is " + strings.ToLower(describePhase(pod)), false
}

func describePhase(pod *corev1.Pod) string {
	if pod.Status.Reason != "" {
		return pod.Status.Reason
	}
	if pod.Status.Phase != "" {
		return string(pod.Status.Phase)
	}
	return "Pending"
}

// deleteJob removes the client and its pod at once. A failure is left to
// the Job's TTL and deadline.
func deleteJob(ctx context.Context, clientset k8s.Interface, namespace, name string) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	propagation := metav1.DeletePropagationBackground
	_ = clientset.BatchV1().Jobs(namespace).Delete(ctx, name, metav1.DeleteOptions{PropagationPolicy: &propagation})
	// Background propagation leaves the pod to the garbage collector;
	// delete it directly so the connection closes now.
	grace := int64(0)
	_ = clientset.CoreV1().Pods(namespace).DeleteCollection(ctx,
		metav1.DeleteOptions{GracePeriodSeconds: &grace},
		metav1.ListOptions{LabelSelector: LabelSession + "=" + name})
}
