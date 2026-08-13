package reconcile

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/module"
)

func TestCertificateState(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	future := now.Add(30 * 24 * time.Hour)
	past := now.Add(-time.Hour)
	cases := []struct {
		name   string
		status module.CertificateStatus
		want   string
	}{
		{"fresh, nothing happened yet", module.CertificateStatus{}, "pending"},
		{"issuing in flight", module.CertificateStatus{Issuing: true}, "issuing"},
		{"failed attempts", module.CertificateStatus{FailedAttempts: 2}, "failing"},
		{"issued and valid", module.CertificateStatus{Ready: true, NotAfter: future}, "active"},
		{"renewal failing on a valid certificate",
			module.CertificateStatus{Ready: false, NotAfter: future}, "failing"},
		{"expired", module.CertificateStatus{Ready: false, NotAfter: past}, "expired"},
		{"expired wins even while ready is stale",
			module.CertificateStatus{Ready: true, NotAfter: past}, "expired"},
	}
	for _, c := range cases {
		require.Equal(t, c.want, certificateState(&c.status, now), c.name)
	}
}

func TestExpressionDisplay(t *testing.T) {
	t.Parallel()
	expression := compiler.Expression{Parts: []compiler.ExpressionPart{
		{Kind: "literal", Value: "api."},
		{Kind: "project_variable", Name: "APP_DOMAIN"},
	}}
	require.Equal(t, "api.${APP_DOMAIN}", expressionDisplay(expression))
}
