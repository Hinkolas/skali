package client

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fastReads paces a test client's read retries in milliseconds.
func fastReads(c *Client) *Client {
	c.reads = readRetry{first: time.Millisecond, max: 4 * time.Millisecond, window: 200 * time.Millisecond}
	return c
}

// failing answers the first failures requests with fail and every later one
// with a run tree, counting every request it sees.
func failing(t *testing.T, failures int64, fail func(w http.ResponseWriter)) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) <= failures {
			fail(w)
			return
		}
		_, _ = w.Write([]byte(`{"run":{"id":"r1","status":"running"},"steps":[]}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func edgeStatus(status int) func(w http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(http.StatusText(status)))
	}
}

func skalidError(status int, code string) func(w http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":{"code":"` + code + `","message":"refused"}}`))
	}
}

// resetConnection aborts the exchange with a TCP reset, as a dying skalid
// pod or a restarting edge does.
func resetConnection(w http.ResponseWriter) {
	conn, _, err := w.(http.Hijacker).Hijack()
	if err != nil {
		panic(err)
	}
	_ = conn.(*net.TCPConn).SetLinger(0)
	_ = conn.Close()
}

// closeConnection ends the exchange without a response.
func closeConnection(w http.ResponseWriter) {
	conn, _, err := w.(http.Hijacker).Hijack()
	if err != nil {
		panic(err)
	}
	_ = conn.Close()
}

func TestReadRetriesTransientFailureThenSucceeds(t *testing.T) {
	for name, fail := range map[string]func(http.ResponseWriter){
		"bad gateway":         edgeStatus(http.StatusBadGateway),
		"service unavailable": edgeStatus(http.StatusServiceUnavailable),
		"gateway timeout":     edgeStatus(http.StatusGatewayTimeout),
		"connection reset":    resetConnection,
		"connection closed":   closeConnection,
	} {
		t.Run(name, func(t *testing.T) {
			srv, hits := failing(t, 2, fail)
			tree, err := fastReads(New(srv.URL, "", Caller{})).GetRun(context.Background(), "r1")
			require.NoError(t, err)
			require.Equal(t, "r1", tree.Run.ID)
			require.EqualValues(t, 3, hits.Load())
		})
	}
}

func TestReadGivesUpAfterItsWindow(t *testing.T) {
	srv, hits := failing(t, 1<<30, edgeStatus(http.StatusServiceUnavailable))
	c := fastReads(New(srv.URL, "", Caller{}))

	started := time.Now()
	_, err := c.GetRun(context.Background(), "r1")
	require.Less(t, time.Since(started), time.Second)
	require.Greater(t, hits.Load(), int64(2))

	require.ErrorContains(t, err, "answered HTTP 503 Service Unavailable; still failing after")
	var apiErr *APIError
	require.ErrorAs(t, err, &apiErr, "the last failure stays inspectable")
	require.Equal(t, http.StatusServiceUnavailable, apiErr.Status)
}

func TestReadNeverWaitsPastTheDeadline(t *testing.T) {
	srv, hits := failing(t, 1<<30, edgeStatus(http.StatusBadGateway))
	c := New(srv.URL, "", Caller{})
	c.reads = readRetry{first: time.Hour, max: time.Hour, window: 2 * time.Hour}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	started := time.Now()
	_, err := c.GetRun(ctx, "r1")
	require.Less(t, time.Since(started), time.Second)
	require.EqualValues(t, 1, hits.Load())
	var apiErr *APIError
	require.ErrorAs(t, err, &apiErr, "the read returns its own failure, not the deadline")
	require.Equal(t, http.StatusBadGateway, apiErr.Status)
}

func TestReadStopsWaitingOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	srv, hits := failing(t, 1<<30, func(w http.ResponseWriter) {
		edgeStatus(http.StatusBadGateway)(w)
		cancel()
	})
	c := New(srv.URL, "", Caller{})
	c.reads = readRetry{first: time.Hour, max: time.Hour, window: 2 * time.Hour}

	started := time.Now()
	_, err := c.GetRun(ctx, "r1")
	require.Less(t, time.Since(started), time.Second)
	require.ErrorIs(t, err, context.Canceled)
	require.EqualValues(t, 1, hits.Load())
}

func TestReadDoesNotRetryFinalFailures(t *testing.T) {
	for name, fail := range map[string]func(http.ResponseWriter){
		"unauthenticated":     skalidError(http.StatusUnauthorized, "unauthenticated"),
		"forbidden":           skalidError(http.StatusForbidden, "forbidden"),
		"not found":           skalidError(http.StatusNotFound, "not_found"),
		"validation":          skalidError(http.StatusBadRequest, "invalid_request"),
		"version mismatch":    skalidError(http.StatusConflict, CodeCLIVersionMismatch),
		"skalid's own 503":    skalidError(http.StatusServiceUnavailable, "backup_target_unconfigured"),
		"internal error":      edgeStatus(http.StatusInternalServerError),
		"undecodable success": func(w http.ResponseWriter) { _, _ = w.Write([]byte("not json")) },
	} {
		t.Run(name, func(t *testing.T) {
			srv, hits := failing(t, 1, fail)
			_, err := fastReads(New(srv.URL, "", Caller{})).GetRun(context.Background(), "r1")
			require.Error(t, err)
			require.EqualValues(t, 1, hits.Load())
		})
	}
}

func TestReadDoesNotRetryAnotherInstallation(t *testing.T) {
	srv, hits := failing(t, 1, func(w http.ResponseWriter) {
		w.Header().Set(InstanceHeader, "install-b")
		edgeStatus(http.StatusBadGateway)(w)
	})
	c := fastReads(New(srv.URL, "", Caller{}))
	c.PinInstance("install-a", nil)

	_, err := c.GetRun(context.Background(), "r1")
	var mismatch *InstanceMismatchError
	require.ErrorAs(t, err, &mismatch)
	require.EqualValues(t, 1, hits.Load())
}

func TestMutationIsNeverReplayed(t *testing.T) {
	for name, fail := range map[string]func(http.ResponseWriter){
		"bad gateway":      edgeStatus(http.StatusBadGateway),
		"connection reset": resetConnection,
	} {
		t.Run(name, func(t *testing.T) {
			srv, hits := failing(t, 1, fail)
			_, err := fastReads(New(srv.URL, "", Caller{})).CancelRun(context.Background(), "r1")
			require.Error(t, err)
			require.EqualValues(t, 1, hits.Load())
		})
	}
}

// A refused connection is a restart only once the master has answered:
// on first contact nothing serves the address, and the read fails fast.
func TestRefusedConnectionRetriesOnlyAfterAnAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"run":{"id":"r1"},"steps":[]}`))
	}))
	base := srv.URL
	srv.Close()

	_, err := fastReads(New(base, "", Caller{})).GetRun(context.Background(), "r1")
	require.ErrorContains(t, err, "unreachable")
	require.NotContains(t, err.Error(), "still failing")

	srv = httptest.NewServer(srv.Config.Handler)
	c := fastReads(New(srv.URL, "", Caller{}))
	_, err = c.GetRun(context.Background(), "r1")
	require.NoError(t, err)
	srv.Close()

	_, err = c.GetRun(context.Background(), "r1")
	require.ErrorContains(t, err, "unreachable")
	require.ErrorContains(t, err, "still failing after")
}
