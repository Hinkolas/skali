package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/checkout"
	"github.com/Hinkolas/skali/internal/cliconfig"
)

func TestResolveQueryTargetFromBinding(t *testing.T) {
	current := newFakeInstall(t)
	bound := newFakeInstall(t)
	bound.seed("p1", "flowdemo", "production", "staging")
	stageRemotes(t, "current", map[string]*cliconfig.Remote{
		"current": {Master: current.srv.URL},
		"bound":   {Master: bound.srv.URL},
	})
	project := testFlowProject(t)
	require.NoError(t, checkout.Save(project.Root, &checkout.Target{
		Master: bound.srv.URL, Project: "flowdemo", Environment: "production"}))

	// The binding supplies remote and environment; no flag needed.
	target, err := resolveQueryTarget(context.Background(), project.Root, "")
	require.NoError(t, err)
	require.Equal(t, "bound", target.remoteName)
	require.Equal(t, bound.srv.URL, target.master)
	require.Equal(t, "flowdemo", target.project)
	require.Equal(t, "production", target.environment)
	require.Equal(t, "p1-e1", target.environmentID)

	// An explicit environment overrides the bound one within the project.
	target, err = resolveQueryTarget(context.Background(), project.Root, "staging")
	require.NoError(t, err)
	require.Equal(t, "staging", target.environment)
	require.Equal(t, "p1-e2", target.environmentID)

	// A name outside the bound project is a precise error.
	_, err = resolveQueryTarget(context.Background(), project.Root, "missing")
	require.ErrorContains(t, err, "environment missing does not exist in project flowdemo")
}

func TestResolveQueryTargetUnboundNeedsEnvironment(t *testing.T) {
	install := newFakeInstall(t)
	stageRemotes(t, "r", map[string]*cliconfig.Remote{"r": {Master: install.srv.URL}})

	_, err := resolveQueryTarget(context.Background(), t.TempDir(), "")
	require.ErrorContains(t, err, "--environment is required")
	require.ErrorContains(t, err, "checkout")
}

func TestResolveQueryTargetUnboundScansCurrentRemote(t *testing.T) {
	install := newFakeInstall(t)
	install.seed("p1", "other-app")
	install.seed("p2", "flowdemo", "production")
	stageRemotes(t, "r", map[string]*cliconfig.Remote{"r": {Master: install.srv.URL}})

	target, err := resolveQueryTarget(context.Background(), t.TempDir(), "production")
	require.NoError(t, err)
	require.Equal(t, "r", target.remoteName)
	require.Empty(t, target.project)
	require.Equal(t, "p2-e1", target.environmentID)

	_, err = resolveQueryTarget(context.Background(), t.TempDir(), "missing")
	require.ErrorContains(t, err, "environment missing not found on this installation")
}

func TestResolveQueryTargetUnknownBoundMaster(t *testing.T) {
	stageRemotes(t, "", map[string]*cliconfig.Remote{})
	project := testFlowProject(t)
	require.NoError(t, checkout.Save(project.Root, &checkout.Target{
		Master: "https://gone.example.com", Project: "flowdemo", Environment: "production"}))

	_, err := resolveQueryTarget(context.Background(), project.Root, "")
	require.ErrorContains(t, err, "no remote for https://gone.example.com on this machine")
	require.ErrorContains(t, err, "skali remote add")
}
