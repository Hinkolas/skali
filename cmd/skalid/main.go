// Command skalid is the skali daemon. On the master it serves the
// client-facing REST API (web BFF, skali CLI, future native clients) plus the
// cluster control plane; on every other node it runs as a worker agent.
//
// Besides serving (the default), the binary carries the operator commands —
// one artifact to deploy and exec into:
//
//	skalid [serve]                          run the master (REST API + cluster control plane)
//	skalid user create|list|set-role|delete manage app users (there is no signup endpoint)
//	skalid migrate up|status                apply / inspect database migrations
//	skalid enroll --master --token          join this machine to a cluster (one-time)
//	skalid agent                            run as a worker node (after enroll)
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/Hinkolas/skali/internal/api"
	"github.com/Hinkolas/skali/internal/auth"
	"github.com/Hinkolas/skali/internal/cluster"
	"github.com/Hinkolas/skali/internal/config"
	"github.com/Hinkolas/skali/internal/engine"
	"github.com/Hinkolas/skali/internal/hostinfo"
	"github.com/Hinkolas/skali/internal/mirror"
	"github.com/Hinkolas/skali/internal/obs"
	"github.com/Hinkolas/skali/internal/store"
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
	case "enroll":
		return runEnroll(args[1:])
	case "agent":
		return runAgent()
	default:
		return fmt.Errorf("unknown command %q (available: serve, user, migrate, enroll, agent)", args[0])
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

	// Container engine before the database: construction is offline (a
	// missing daemon fails per-operation, not here), and a future milestone
	// boots the master's own control-plane Postgres through this engine
	// before the connection below can exist.
	eng, err := engine.NewDocker(cfg.EngineSocket)
	if err != nil {
		return err
	}
	defer eng.Close()

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

	// Cluster plane: CA, the master's own node row, the enrollment gRPC
	// listener, and the worker heartbeat poller.
	ca, err := cluster.EnsureCA(ctx, st, cfg.AuthSecret)
	if err != nil {
		return err
	}
	self, err := cluster.EnsureSelfNode(ctx, st, cfg.ClusterAddr)
	if err != nil {
		return err
	}
	clusterSvc := cluster.NewService(st, ca, cfg.ClusterAddr)

	// The image mirror follows CLUSTER_ADDR: no cluster address, no registry
	// (single-node dev keeps working with zero setup).
	regAddr := registryAddr(cfg)
	if regAddr == "" {
		slog.InfoContext(ctx, "registry disabled: CLUSTER_ADDR unset")
	}

	grpcSrv, err := cluster.NewMasterServer(st, ca, clusterCertHosts(cfg.ClusterAddr), regAddr)
	if err != nil {
		return err
	}
	grpcLis, err := net.Listen("tcp", cfg.GRPCAddr)
	if err != nil {
		return err
	}
	defer grpcSrv.GracefulStop()

	// The master's own resource sampler ("/" until a data dir exists) feeds
	// its node row via the poller's self-stamp, and the OTel gauges. The
	// container sampler does the same for the master's own containers.
	sampler := hostinfo.New("/")
	if err := hostinfo.RegisterGauges(sampler); err != nil {
		return err
	}
	containers := engine.NewSampler(eng)
	inventory := engine.NewInventorySampler(eng)
	// Engine events resample immediately, so observed state doesn't wait for
	// a sampler tick.
	notifier := engine.NewNotifier(eng, containers, inventory)

	// One connection per node, shared by the poller and container lifecycle
	// calls.
	conns, err := cluster.NewConnPool(ca)
	if err != nil {
		return err
	}
	defer conns.Close()

	poller, err := cluster.NewPoller(st, conns, self.ID, sampler, containers, inventory, 0, 0)
	if err != nil {
		return err
	}
	// Doorbells: one WatchEvents stream per worker, plus the master's own
	// notifier consumed locally — every ring becomes an immediate resync.
	watcher := cluster.NewWatcher(st, conns, self.ID, poller.Poke)

	containerOps := cluster.NewContainerOps(st, conns, self.ID, eng)

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           api.NewRouter(api.Deps{Auth: authSvc, Store: st, DB: pool, Cluster: clusterSvc, Containers: containerOps}),
		ReadHeaderTimeout: 5 * time.Second,
	}
	serveErr := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
	}()
	go func() {
		if err := grpcSrv.Serve(grpcLis); err != nil {
			serveErr <- err
		}
	}()

	loopCtx, cancelLoops := context.WithCancel(ctx)
	defer cancelLoops()
	go sweepLoop(loopCtx, authSvc)
	go sampler.Run(loopCtx)
	go containers.Run(loopCtx)
	go inventory.Run(loopCtx)
	go notifier.Run(loopCtx)
	go poller.Run(loopCtx)
	go watcher.Run(loopCtx)
	if regAddr != "" {
		// The registry container (retried until the engine is up) and the
		// master's own docker trust for it — the master pulls from the
		// mirror too. Trust failure is a warning: only mirror pulls need it.
		go mirror.EnsureLoop(loopCtx, eng, ca, mirror.Config{
			Endpoint: regAddr, DataDir: cfg.DataDir, Image: cfg.RegistryImage, Port: cfg.RegistryPort,
		})
		if certPEM, keyPEM, err := ca.ClientPEM(); err != nil {
			slog.WarnContext(ctx, "issue master registry client cert", "err", err)
		} else if err := mirror.InstallDockerCerts(mirror.DefaultCertsDir, regAddr, ca.CAPEM(), certPEM, keyPEM); err != nil {
			slog.WarnContext(ctx, "install docker trust for the cluster registry", "err", err)
		}
	}
	go func() {
		bell, cancel := notifier.Subscribe()
		defer cancel()
		for {
			select {
			case <-loopCtx.Done():
				return
			case <-bell:
				poller.Poke(self.ID)
			}
		}
	}()

	slog.InfoContext(ctx, "starting", "service", serviceName,
		"http_addr", cfg.HTTPAddr, "grpc_addr", cfg.GRPCAddr, "node_id", self.ID)

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

// clusterCertHosts derives the SANs for the master's gRPC listener cert from
// CLUSTER_ADDR (loopback is always appended by NewMasterServer).
func clusterCertHosts(clusterAddr string) []string {
	if clusterAddr == "" {
		return nil
	}
	host, _, err := net.SplitHostPort(clusterAddr)
	if err != nil {
		return nil
	}
	return []string{host}
}

// registryAddr derives the image mirror's endpoint: CLUSTER_ADDR's host on
// REGISTRY_PORT. Empty (registry disabled) while CLUSTER_ADDR is unset.
func registryAddr(cfg *config.API) string {
	hosts := clusterCertHosts(cfg.ClusterAddr)
	if len(hosts) == 0 {
		return ""
	}
	return net.JoinHostPort(hosts[0], strconv.Itoa(cfg.RegistryPort))
}

func shutdownWithin(fn func(context.Context) error, d time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	if err := fn(ctx); err != nil {
		slog.Error("shutdown", "service", serviceName, "err", err)
	}
}
