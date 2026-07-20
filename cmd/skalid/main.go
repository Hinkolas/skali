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

	"github.com/Hinkolas/skali/internal/api"
	"github.com/Hinkolas/skali/internal/artifactstore"
	"github.com/Hinkolas/skali/internal/auth"
	"github.com/Hinkolas/skali/internal/config"
	"github.com/Hinkolas/skali/internal/deploy"
	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/obs"
	"github.com/Hinkolas/skali/internal/project"
	"github.com/Hinkolas/skali/internal/store"
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
	deploySvc := deploy.New(st, valueSvc, artifactSvc, versionpkg.Version)

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

	srv := &http.Server{
		Addr: cfg.HTTPAddr,
		Handler: api.NewRouter(api.Deps{
			Auth:     authSvc,
			Store:    st,
			DB:       pool,
			Projects: projectSvc,
			Values:   valueSvc,
			Deploy:   deploySvc,
			Journal:  journalSvc,
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

	// Staged values and pending artifact records are normally closed
	// explicitly; the sweeps are the safety net for abandoned candidates
	// and dead executors. Run at boot and hourly.
	if _, err := valueSvc.SweepStaged(ctx, 24*time.Hour); err != nil {
		slog.WarnContext(ctx, "sweep staged values", "err", err)
	}
	if _, err := artifactSvc.SweepPending(ctx, 24*time.Hour); err != nil {
		slog.WarnContext(ctx, "sweep pending artifacts", "err", err)
	}
	go productSweepLoop(loopCtx, valueSvc, artifactSvc)

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

func productSweepLoop(ctx context.Context, valueSvc *valuestore.Service, artifactSvc *artifactstore.Service) {
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
