package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Hinkolas/skali/internal/cluster"
	"github.com/Hinkolas/skali/internal/config"
	"github.com/Hinkolas/skali/internal/engine"
	"github.com/Hinkolas/skali/internal/hostinfo"
	"github.com/Hinkolas/skali/internal/obs"
)

// runAgent runs the worker daemon: the NodeService gRPC server the master
// dials. Like enroll it loads only config.Agent — a worker's state is its
// enrolled identity, never a database.
func runAgent() error {
	ctx := context.Background()

	cfg, err := config.Load[config.Agent](ctx)
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

	identity, err := cluster.LoadIdentity(cfg.DataDir)
	if err != nil {
		return err
	}

	sigCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Resource sampler: feeds heartbeat metrics and (when an OTLP endpoint is
	// configured) the OTel gauges.
	sampler := hostinfo.New(cfg.DataDir)
	if err := hostinfo.RegisterGauges(sampler); err != nil {
		return err
	}
	go sampler.Run(sigCtx)

	// Container engine + observers. Construction is offline and the samplers
	// tolerate an unreachable engine (heartbeats report "unknown"), so an
	// agent whose dockerd is still booting comes up fine.
	eng, err := engine.NewDocker(cfg.EngineSocket)
	if err != nil {
		return err
	}
	defer eng.Close()
	containers := engine.NewSampler(eng)
	go containers.Run(sigCtx)
	inventory := engine.NewInventorySampler(eng)
	go inventory.Run(sigCtx)
	// Engine events resample immediately, so observed state doesn't wait for
	// a sampler tick.
	notifier := engine.NewNotifier(eng, containers, inventory)
	go notifier.Run(sigCtx)

	err = cluster.ServeAgent(sigCtx, identity, cfg.GRPCAddr, sampler, eng, containers, inventory)
	if sigCtx.Err() != nil {
		slog.Info("shutdown signal received", "service", serviceName)
	}
	return err
}
