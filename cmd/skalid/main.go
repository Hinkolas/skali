// Command skalid is the skali control plane: the client-facing REST API (web
// BFF, skali CLI, future native clients) plus the controller that compiles
// services into Kubernetes objects and reads status back.
//
// Besides serving (the default), the binary carries the operator commands —
// one artifact to deploy and exec into:
//
//	skalid [serve]                          run the control plane (REST API + controller)
//	skalid user create|list|set-role|delete manage app users (there is no signup endpoint)
//	skalid migrate up|status                apply / inspect database migrations
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Hinkolas/skali/internal/api"
	"github.com/Hinkolas/skali/internal/artifactstore"
	"github.com/Hinkolas/skali/internal/auth"
	"github.com/Hinkolas/skali/internal/buildstore"
	"github.com/Hinkolas/skali/internal/config"
	"github.com/Hinkolas/skali/internal/dbstore"
	"github.com/Hinkolas/skali/internal/deploy"
	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/module"
	"github.com/Hinkolas/skali/internal/module/app"
	"github.com/Hinkolas/skali/internal/module/bucket"
	"github.com/Hinkolas/skali/internal/module/database"
	"github.com/Hinkolas/skali/internal/obs"
	"github.com/Hinkolas/skali/internal/observe"
	"github.com/Hinkolas/skali/internal/project"
	"github.com/Hinkolas/skali/internal/reconcile"
	"github.com/Hinkolas/skali/internal/registry"
	"github.com/Hinkolas/skali/internal/registrytoken"
	"github.com/Hinkolas/skali/internal/runtimelogs"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/substrate"
	"github.com/Hinkolas/skali/internal/substrate/cnpg"
	"github.com/Hinkolas/skali/internal/substrate/seaweed"
	"github.com/Hinkolas/skali/internal/valuestore"
	versionpkg "github.com/Hinkolas/skali/internal/version"
)

const serviceName = "skalid"

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "service", serviceName, "err", err)
		os.Exit(1)
	}
}

func run() error {
	args := os.Args[1:]
	if len(args) == 0 || args[0] == "serve" {
		return runServe()
	}
	switch args[0] {
	case "user":
		return runUser(args[1:])
	case "migrate":
		return runMigrate(args[1:])
	default:
		return fmt.Errorf("unknown command %q (available: serve, user, migrate)", args[0])
	}
}

func runServe() error {
	ctx := context.Background()

	cfg, err := config.Load[config.API](ctx)
	if err != nil {
		return err
	}

	shutdownObs, err := obs.Init(ctx, obs.Options{
		ServiceName: serviceName,
		LogLevel:    cfg.LogLevel,
		LogFormat:   cfg.LogFormat,
		LogOutput:   cfg.LogOutput,
		LogFile:     cfg.LogFile,
	})
	if err != nil {
		return err
	}
	defer shutdownWithin(shutdownObs, 5*time.Second)

	pool, err := store.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	st := store.NewStore(pool)

	authSvc, err := auth.New(st, auth.Config{Secret: cfg.AuthSecret, ReauthWindow: cfg.ReauthWindow})
	if err != nil {
		return err
	}
	projectSvc := project.New(st)
	valueSvc, err := valuestore.New(st, cfg.AuthSecret)
	if err != nil {
		return err
	}
	artifactSvc := artifactstore.New(st)
	buildSvc := buildstore.New(st)
	deploySvc := deploy.New(st, valueSvc, artifactSvc, versionpkg.Version)

	// The managed-registry client; an empty SKALI_REGISTRY_HOST disables
	// the build and import surfaces (API-only or values-only development).
	registryClient := &registry.Client{
		Host:     cfg.RegistryHost,
		Endpoint: cfg.RegistryEndpoint,
		PushHost: cfg.RegistryPushHost,
		Insecure: cfg.RegistryInsecure,
	}

	// The registry token signer exists only on installations whose registry
	// requires token auth (production); skalid then serves the token realm
	// and self-issues pull tokens for digest verification.
	var tokenSigner *registrytoken.Signer
	if cfg.RegistryTokenKey != "" {
		tokenSigner, err = registrytoken.LoadSigner([]byte(cfg.RegistryTokenKey))
		if err != nil {
			return err
		}
		registryClient.TokenSource = func(repository string, actions []string) (string, error) {
			access := []registrytoken.Access{{Type: "repository", Name: repository, Actions: actions}}
			return tokenSigner.Mint(registrytoken.Service, registrytoken.Issuer, access,
				time.Now(), registrytoken.TokenTTL)
		}
	}

	// A fresh executor identity per boot: recovery fails attempts owned by
	// executors that no longer exist.
	executorID, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("generate executor id: %w", err)
	}
	journalSvc := journal.NewService(st, executorID.String())
	if failed, err := journalSvc.RecoverOnBoot(ctx); err != nil {
		return fmt.Errorf("recover journal: %w", err)
	} else if failed > 0 {
		slog.InfoContext(ctx, "recovered orphaned attempts", "failed", failed)
	}

	// Cluster access is optional in development: without SKALI_KUBECONFIG or
	// in-cluster credentials skalid runs API-only, observation reports
	// unknown, and the reconcile workers idle.
	kubeClient, err := kube.New(cfg.KubeconfigPath)
	if err != nil {
		if !errors.Is(err, kube.ErrNoCluster) {
			return err
		}
		slog.InfoContext(ctx, "no cluster configuration resolved; running API-only")
	}

	// The production service modules.
	registry := module.NewRegistry()
	if err := registry.Register(app.Module{}); err != nil {
		return fmt.Errorf("register application module: %w", err)
	}
	if err := registry.Register(database.Module{}); err != nil {
		return fmt.Errorf("register database module: %w", err)
	}
	if err := registry.Register(bucket.Module{}); err != nil {
		return fmt.Errorf("register bucket module: %w", err)
	}

	observed := observe.NewStore(nil)
	var kernel *reconcile.Kernel
	var source *observe.KubeSource
	if kubeClient != nil {
		source = observe.NewKubeSource(kubeClient, observed, observe.SourceOptions{
			Resync:         cfg.ReconcileResync,
			StaleThreshold: cfg.StaleThreshold,
			Enqueue:        func(environmentID uuid.UUID) { kernel.Enqueue(environmentID) },
			Dynamic:        cnpg.ObserveKinds(),
		})
	}
	kernelDeps := reconcile.Deps{
		Store:    st,
		Deploy:   deploySvc,
		Values:   valueSvc,
		Journal:  journalSvc,
		Registry: registry,
		Observed: observed,
		Source:   source,
	}
	if kubeClient != nil {
		kernelDeps.Cluster = kubeClient
		kernelDeps.JobLogs = kubeClient.TailJobLogs
	}
	// The platform substrate controller runs beside the kernel with its own
	// queue: it owns pools, tenants, the object store, and credentials in
	// skali-platform and pokes the kernel when a claim's outputs become
	// ready. The kernel records desired claims through it (Deps.Claims).
	var substrateCtl *substrate.Controller
	if kubeClient != nil {
		substrateCtl = substrate.New(substrate.Deps{
			DB:       dbstore.New(st),
			Cluster:  substrate.KubeCluster{Client: kubeClient},
			Observed: observed,
			Seaweed:  seaweed.NewClient(kubeClient, substrate.Namespace),
			Enqueue:  func(environmentID uuid.UUID) { kernel.Enqueue(environmentID) },
		}, substrate.Config{
			Managed:      cfg.ManagedCluster,
			Capabilities: cfg.Capabilities,
			S3Domain:     cfg.S3Domain,
			Resync:       cfg.ReconcileResync,
		})
		kernelDeps.Claims = substrateCtl
	}
	reconcileCfg := reconcile.Config{
		Resync:          cfg.ReconcileResync,
		Audit:           cfg.ReconcileAudit,
		RolloutDeadline: cfg.RolloutDeadline,
		StaleThreshold:  cfg.StaleThreshold,
		ManagedCluster:  cfg.ManagedCluster,
	}
	kernel = reconcile.New(kernelDeps, reconcileCfg)
	deploySvc.SetEnqueuer(kernel)

	runtimeLogs := &runtimelogs.Streamer{Observed: observed, Store: st}
	// The sanctioned request-time Secret read behind credential reveal.
	var secretReader func(ctx context.Context, namespace, name string) (map[string][]byte, error)
	if kubeClient != nil {
		runtimeLogs.Clientset = kubeClient.Clientset
		secretReader = func(ctx context.Context, namespace, name string) (map[string][]byte, error) {
			secret, err := kubeClient.Clientset.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return nil, err
			}
			return secret.Data, nil
		}
	}
	srv := &http.Server{
		Addr: cfg.HTTPAddr,
		Handler: api.NewRouter(api.Deps{
			Auth:               authSvc,
			Store:              st,
			DB:                 pool,
			Projects:           projectSvc,
			Values:             valueSvc,
			Deploy:             deploySvc,
			Artifacts:          artifactSvc,
			Builds:             buildSvc,
			Journal:            journalSvc,
			Reconcile:          kernel,
			Registry:           registryClient,
			RegistryToken:      tokenSigner,
			RegistryNodeSecret: cfg.RegistryNodeSecret,
			RuntimeLogs:        runtimeLogs,
			Capabilities:       cfg.Capabilities,
			Databases:          dbstore.New(st),
			SecretReader:       secretReader,
		}),
		ReadHeaderTimeout: 5 * time.Second,
	}
	serveErr := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
	}()

	loopCtx, cancelLoops := context.WithCancel(ctx)
	defer cancelLoops()
	go sweepLoop(loopCtx, authSvc)

	// The reconciliation kernel: observation sync, workers, and audits. In
	// API-only mode it parks until shutdown.
	go func() {
		if err := kernel.Run(loopCtx); err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("reconcile kernel stopped", "err", err)
		}
	}()
	if substrateCtl != nil {
		go substrateCtl.Run(loopCtx)
		// The SeaweedFS provider observer (REWORK_V2 7.4): poll-based, its
		// own named source, so a seaweed outage degrades bucket health
		// without touching cluster observation.
		seaweedPoll := observe.NewPollSource(observed, substrateCtl.SeaweedProbe(), observe.PollOptions{
			Source:  seaweed.SourceName,
			Enqueue: func(environmentID uuid.UUID) { kernel.Enqueue(environmentID) },
		})
		substrateCtl.SetProbePoke(seaweedPoll.Poke)
		go func() {
			if err := seaweedPoll.Run(loopCtx); err != nil && !errors.Is(err, context.Canceled) {
				slog.Error("seaweed observer stopped", "err", err)
			}
		}()
	}

	// Staged values and pending artifact records are normally closed
	// explicitly; the sweeps are the safety net for abandoned candidates
	// and dead executors. Run at boot and hourly.
	if _, err := valueSvc.SweepStaged(ctx, 24*time.Hour); err != nil {
		slog.WarnContext(ctx, "sweep staged values", "err", err)
	}
	if _, err := artifactSvc.SweepPending(ctx, 24*time.Hour); err != nil {
		slog.WarnContext(ctx, "sweep pending artifacts", "err", err)
	}
	go productSweepLoop(loopCtx, valueSvc, artifactSvc, deploySvc, journalSvc, cfg.BuildStaleTimeout)

	slog.InfoContext(ctx, "starting", "service", serviceName, "http_addr", cfg.HTTPAddr)

	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	select {
	case err := <-serveErr:
		return err
	case <-sigCtx.Done():
	}
	slog.Info("shutdown signal received", "service", serviceName)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// sweepLoop hourly clears expired sessions and login challenges.
func sweepLoop(ctx context.Context, svc *auth.Service) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := svc.SweepExpired(ctx); err != nil {
				slog.WarnContext(ctx, "sweep expired auth rows", "err", err)
			}
		}
	}
}

func productSweepLoop(ctx context.Context, valueSvc *valuestore.Service, artifactSvc *artifactstore.Service,
	deploySvc *deploy.Service, journalSvc *journal.Service, buildStaleTimeout time.Duration) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := valueSvc.SweepStaged(ctx, 24*time.Hour); err != nil {
				slog.WarnContext(ctx, "sweep staged values", "err", err)
			}
			if _, err := artifactSvc.SweepPending(ctx, 24*time.Hour); err != nil {
				slog.WarnContext(ctx, "sweep pending artifacts", "err", err)
			}
			// Abandoned artifact windows: the client stopped building or
			// verifying; fail the deployment and free the environment.
			if swept, err := deploySvc.SweepStaleDeployments(ctx, journalSvc, buildStaleTimeout); err != nil {
				slog.WarnContext(ctx, "sweep stale deployments", "err", err)
			} else if swept > 0 {
				slog.InfoContext(ctx, "swept stale deployments", "count", swept)
			}
		}
	}
}

func shutdownWithin(fn func(context.Context) error, d time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	if err := fn(ctx); err != nil {
		slog.Error("shutdown", "service", serviceName, "err", err)
	}
}
