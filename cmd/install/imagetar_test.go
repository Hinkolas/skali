package main

import (
	"archive/tar"
	"bytes"
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/clirender"
	"github.com/Hinkolas/skali/internal/installer/host"
)

// discardProgress renders onto /dev/null; stageSkalidImage needs a live
// task printer.
func discardProgress(t *testing.T) *taskProgress {
	t.Helper()
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	require.NoError(t, err)
	t.Cleanup(func() { devnull.Close() })
	return newTaskProgress(clirender.NewTasks(devnull))
}

func imageTarFixture(t *testing.T, manifest string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	require.NoError(t, writer.WriteHeader(&tar.Header{
		Name: "manifest.json", Mode: 0o644, Size: int64(len(manifest)),
	}))
	_, err := writer.Write([]byte(manifest))
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	return buffer.Bytes()
}

const imageTarHex = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestParseImageTarManifest(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		manifest string
		image    string
		imageID  string
		wantErr  string
	}{
		{
			name:     "classic config name",
			manifest: `[{"Config":"` + imageTarHex + `.json","RepoTags":["skalid:dev"]}]`,
			image:    "skalid:dev",
			imageID:  "sha256:" + imageTarHex,
		},
		{
			name:     "oci blob config name",
			manifest: `[{"Config":"blobs/sha256/` + imageTarHex + `","RepoTags":["skalid:dev"]}]`,
			image:    "skalid:dev",
			imageID:  "sha256:" + imageTarHex,
		},
		{
			name:     "unrecognized config keeps the image",
			manifest: `[{"Config":"weird","RepoTags":["skalid:dev"]}]`,
			image:    "skalid:dev",
		},
		{
			name:     "no repo tag",
			manifest: `[{"Config":"x.json","RepoTags":[]}]`,
			wantErr:  "carries no repo tag",
		},
		{
			name:     "multiple images",
			manifest: `[{"Config":"a.json","RepoTags":["a:1"]},{"Config":"b.json","RepoTags":["b:1"]}]`,
			wantErr:  "contains 2 images",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			image, imageID, err := parseImageTarManifest(imageTarFixture(t, testCase.manifest))
			if testCase.wantErr != "" {
				require.ErrorContains(t, err, testCase.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, testCase.image, image)
			require.Equal(t, testCase.imageID, imageID)
		})
	}
}

func TestParseImageTarManifestNotATar(t *testing.T) {
	t.Parallel()
	_, _, err := parseImageTarManifest([]byte("not a tar"))
	require.Error(t, err)
}

func TestStageSkalidImage(t *testing.T) {
	tarPath := t.TempDir() + "/skalid-dev.tar"
	data := imageTarFixture(t, `[{"Config":"`+imageTarHex+`.json","RepoTags":["skalid:dev"]}]`)
	require.NoError(t, os.WriteFile(tarPath, data, 0o644))

	fake := &host.Fake{Handlers: map[string]func(host.Command) (host.Result, error){
		"k3s": func(cmd host.Command) (host.Result, error) {
			require.Equal(t, []string{"ctr", "images", "import", "/tmp/skali-image-import.tar"}, cmd.Args)
			return host.Result{}, nil
		},
	}}
	// On Linux readHostFile goes through the runner; darwin reads the file
	// directly. Feed the fake filesystem so both paths resolve.
	require.NoError(t, fake.WriteFile(context.Background(), tarPath, data, 0o644))

	original := activeRunner
	activeRunner = fake
	defer func() { activeRunner = original }()

	image, imageID, err := stageSkalidImage(context.Background(), fake, tarPath, discardProgress(t))
	require.NoError(t, err)
	require.Equal(t, "skalid:dev", image)
	require.Equal(t, "sha256:"+imageTarHex, imageID)
	// The staged copy is written before the import and removed afterwards.
	require.Equal(t, []string{
		"write " + tarPath, "write /tmp/skali-image-import.tar", "remove /tmp/skali-image-import.tar",
	}, fake.Writes)
}

func TestStageSkalidImageImportFailure(t *testing.T) {
	tarPath := t.TempDir() + "/skalid-dev.tar"
	data := imageTarFixture(t, `[{"Config":"x.json","RepoTags":["skalid:dev"]}]`)
	require.NoError(t, os.WriteFile(tarPath, data, 0o644))

	fake := &host.Fake{Handlers: map[string]func(host.Command) (host.Result, error){
		"k3s": func(host.Command) (host.Result, error) {
			return host.Result{ExitCode: 1, Stderr: "ctr: image import failed"}, nil
		},
	}}
	require.NoError(t, fake.WriteFile(context.Background(), tarPath, data, 0o644))
	original := activeRunner
	activeRunner = fake
	defer func() { activeRunner = original }()

	_, _, err := stageSkalidImage(context.Background(), fake, tarPath, discardProgress(t))
	require.ErrorContains(t, err, "k3s ctr images import failed with exit code 1")
	require.ErrorContains(t, err, "ctr: image import failed")
}
