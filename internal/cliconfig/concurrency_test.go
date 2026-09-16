package cliconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConcurrentConfigMutationsAreLossless(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path, err := Path()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
	require.NoError(t, os.WriteFile(path, []byte("future: {enabled: true}\nremotes:\n  main:\n    master: https://main\n    token: old\n    instance: i\n    future: preserve\n"), 0600))
	login, err := Load()
	require.NoError(t, err)
	login.Remotes["main"].Token = "new"
	expected := Remote{Master: "https://main", Token: "old", Instance: "i"}
	var wg sync.WaitGroup
	results := make(chan error, 25)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cfg, err := Load()
			if err != nil {
				results <- err
				return
			}
			cfg.CurrentRemote = fmt.Sprint(i)
			cfg.Remotes[fmt.Sprint(i)] = &Remote{Master: fmt.Sprintf("https://remote-%d", i)}
			results <- Save(cfg)
		}(i)
	}
	wg.Add(1)
	go func() { defer wg.Done(); results <- Observe("main", expected, "i", "v0.4.0") }()
	wg.Wait()
	require.NoError(t, Save(login))
	close(results)
	for err := range results {
		require.NoError(t, err)
	}
	cfg, err := Load()
	require.NoError(t, err)
	require.Len(t, cfg.Remotes, 21)
	require.Equal(t, "new", cfg.Remotes["main"].Token)
	require.Equal(t, "v0.4.0", cfg.Remotes["main"].Version)
	require.Equal(t, "preserve", cfg.Remotes["main"].Extra["future"])
	require.Contains(t, cfg.Extra, "future")
	require.NoError(t, Observe("main", expected, "different", "v9.0.0"))
	cfg, err = Load()
	require.NoError(t, err)
	require.Equal(t, "i", cfg.Remotes["main"].Instance)
	require.Equal(t, "v0.4.0", cfg.Remotes["main"].Version)
	stale := cfg
	require.NoError(t, Update(func(c *Config) error { delete(c.Remotes, "main"); return nil }))
	require.NoError(t, Observe("main", *stale.Remotes["main"], "i", "v9.0.0"))
	stale.Remotes["main"].Token = "late-login"
	require.ErrorContains(t, Save(stale), "changed concurrently")
	cfg, err = Load()
	require.NoError(t, err)
	require.NotContains(t, cfg.Remotes, "main")
}
