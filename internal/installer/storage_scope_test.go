package installer

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/installer/host"
)

func TestRemovedStorageRefusedBeforeHostChanges(t *testing.T) {
	t.Parallel()
	for _, driver := range []string{"longhorn", "unknown"} {
		t.Run(driver, func(t *testing.T) {
			record := liveProfileRecord()
			record.StorageDriver = driver
			fake := &host.Fake{}
			ctx := context.Background()

			// No kube client or progress sink is needed: unsupported storage
			// must be rejected before reading secrets or changing the host.
			_, err := Init(ctx, fake, record, InitOptions{})
			require.ErrorContains(t, err, "no longer supported")
			_, _, err = LiveProfile(ctx, nil, fake, record)
			require.ErrorContains(t, err, "no longer supported")
			err = UpgradeK3s(ctx, fake, record, nil)
			require.ErrorContains(t, err, "no longer supported")
			_, err = record.CanonicalYAML()
			require.ErrorContains(t, err, "no longer supported")
			require.Empty(t, fake.Commands)
			require.Empty(t, fake.Writes)
		})
	}
}

func TestLegacyLocalStorageRecordStillConverges(t *testing.T) {
	t.Parallel()
	for _, driver := range []string{"", "local"} {
		t.Run(driver, func(t *testing.T) {
			record := liveProfileRecord()
			record.StorageDriver = driver
			fake := &host.Fake{FS: map[string][]byte{
				K3sRegistriesPath: []byte(k3sRegistriesYAML("pull-secret-value")),
			}}
			profile, _, err := LiveProfile(context.Background(),
				fakeClientWith(liveProfileObjects()...), fake, record)
			require.NoError(t, err)
			canonical, err := record.CanonicalYAML()
			require.NoError(t, err)
			require.Equal(t, canonical, profile.Production.InstallationRecord)
			require.Equal(t, driver, record.StorageDriver)
		})
	}
}
