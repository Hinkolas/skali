package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/clirender"
	"github.com/Hinkolas/skali/internal/localdev"
	"github.com/Hinkolas/skali/internal/truststore"
)

func TestDevTrustRequiresAnInstalledPlatform(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	var out bytes.Buffer
	err := runDevTrust(context.Background(), &out, true)
	require.ErrorContains(t, err, "run skali dev first")
}

func TestPrintTrustStatusListsEveryStore(t *testing.T) {
	var out bytes.Buffer
	ca := &localdev.CA{CertPath: "/state/ca.crt", CommonName: "skali local dev CA (skali-dev, 1a2b3c4d)"}
	printTrustStatus(&out, clirender.StyleFor(&out), truststore.Status{Stores: []truststore.StoreStatus{
		{Name: "system trust anchors", State: truststore.StateTrusted, Detail: "installed now"},
		{Name: "NSS database ~/.pki/nssdb", State: truststore.StateUnavailable, Detail: "install certutil"},
		{Name: "NSS database ~/.mozilla/firefox/x", State: truststore.StateFailed, Detail: "certutil -A: boom"},
	}}, ca)
	text := out.String()
	require.Contains(t, text, "system trust anchors               trusted  installed now")
	require.Contains(t, text, "NSS database ~/.pki/nssdb          unavailable  install certutil")
	require.Contains(t, text, "NSS database ~/.mozilla/firefox/x  failed  certutil -A: boom")
	require.Contains(t, text, "CA certificate: /state/ca.crt")
	require.Contains(t, text, "subject:        skali local dev CA (skali-dev, 1a2b3c4d)")
}
