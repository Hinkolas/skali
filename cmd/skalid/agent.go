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

	err = cluster.ServeAgent(sigCtx, identity, cfg.GRPCAddr, sampler)
	if sigCtx.Err() != nil {
		slog.Info("shutdown signal received", "service", serviceName)
	}
	return err
}
