package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/deploy"
)

// Refused stateful transitions reach clients with their own codes and the
// deploy package's message, without its prefix: an unsupported move and a
// quota below usage are the request's fault, an unmeasurable reduction is
// worth a retry.
func TestWriteDeployErrorTransitions(t *testing.T) {
	t.Parallel()
	cases := []struct {
		err     error
		status  int
		code    string
		service string
	}{
		{&deploy.TransitionError{Service: "databases.main", Field: "version", From: "17", To: "18"},
			http.StatusUnprocessableEntity, "unsupported_transition", "databases.main.version"},
		{&deploy.BucketQuotaError{Bucket: "buckets.media", UsedBytes: 8 << 30, QuotaBytes: 5 << 30},
			http.StatusUnprocessableEntity, "quota_below_usage", "buckets.media"},
		{&deploy.BucketUsageUnavailableError{Bucket: "buckets.media", QuotaBytes: 5 << 30, Err: errors.New("unreachable")},
			http.StatusServiceUnavailable, "usage_unavailable", "buckets.media"},
	}
	for _, tc := range cases {
		recorder := httptest.NewRecorder()
		writeDeployError(context.Background(), recorder, fmt.Errorf("open: %w", tc.err))
		require.Equal(t, tc.status, recorder.Code, tc.code)
		var body map[string]any
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
		require.Equal(t, tc.code, errCode(body))
		require.NotContains(t, errMessage(body), "deploy:")
		require.Contains(t, errMessage(body), tc.service)
	}
}
