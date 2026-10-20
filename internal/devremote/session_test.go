package devremote

import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSessionLifecycle(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	proxy, err := ProxySession()
	require.NoError(t, err)

	record, running, err := proxy.Running()
	require.NoError(t, err)
	require.False(t, running)
	require.Nil(t, record)
	_, err = proxy.Load()
	require.True(t, errors.Is(err, os.ErrNotExist))

	release, err := proxy.Acquire()
	require.NoError(t, err)
	started := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, proxy.Save(Record{Host: "mac-mini", Fingerprint: "sha256:00", PID: 42, Started: started}))
	_, err = proxy.Acquire()
	require.ErrorIs(t, err, ErrRunning)

	record, running, err = proxy.Running()
	require.NoError(t, err)
	require.True(t, running)
	require.Equal(t, &Record{Host: "mac-mini", Fingerprint: "sha256:00", PID: 42, Started: started}, record)

	// The sessions are independent: a machine may run both.
	expose, err := ExposeSession()
	require.NoError(t, err)
	_, running, err = expose.Running()
	require.NoError(t, err)
	require.False(t, running)

	release()
	_, running, err = proxy.Running()
	require.NoError(t, err)
	require.False(t, running)
	record, err = proxy.Load()
	require.NoError(t, err)
	require.Equal(t, "mac-mini", record.Host, "the target outlives the process")
}
