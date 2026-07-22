package main

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/Hinkolas/skali/internal/installer/host"
)

// stageSkalidImage imports a docker-save tar into the node's containerd
// through the runner and returns the image reference and id it carries.
// This is the source-install path: a checkout builds skalid:dev locally,
// and no fresh node could pull it from a registry. The tar lives on the
// machine the operator runs the installer on (the Mac in darwin mode).
func stageSkalidImage(ctx context.Context, runner host.Runner, tarPath string,
	progress *taskProgress) (string, string, error) {
	data, image, imageID, err := loadImageTar(ctx, tarPath)
	if err != nil {
		return "", "", err
	}
	if err := importImageTar(ctx, runner, data, image, progress); err != nil {
		return "", "", err
	}
	return image, imageID, nil
}

// loadImageTar reads the tar and names the image it carries without
// touching the node; upgrade names the image in its plan before anything
// is imported.
func loadImageTar(ctx context.Context, tarPath string) (data []byte, image, imageID string, err error) {
	data, err = readHostFile(ctx, tarPath)
	if err != nil {
		return nil, "", "", fmt.Errorf("read image tar %s: %w", tarPath, err)
	}
	image, imageID, err = parseImageTarManifest(data)
	if err != nil {
		return nil, "", "", fmt.Errorf("image tar %s: %w", tarPath, err)
	}
	return data, image, imageID, nil
}

// importImageTar writes the tar into the node and imports it into
// containerd.
func importImageTar(ctx context.Context, runner host.Runner, data []byte, image string,
	progress *taskProgress) error {
	progress.Start("Import skalid image " + image)
	const remote = "/tmp/skali-image-import.tar"
	if err := runner.WriteFile(ctx, remote, data, 0o600); err != nil {
		return err
	}
	result, err := runner.Run(ctx, host.Command{
		Name: "k3s", Args: []string{"ctr", "images", "import", remote},
	})
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("k3s ctr images import failed with exit code %d: %s",
			result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	return runner.Remove(ctx, remote)
}

// imageTarManifestEntry is the slice of a docker-save manifest.json this
// command consumes.
type imageTarManifestEntry struct {
	Config   string   `json:"Config"`
	RepoTags []string `json:"RepoTags"`
}

// parseImageTarManifest reads the image reference and content id out of a
// docker-save tar. The id pins the content behind the mutable dev tag so a
// rebuilt image rolls the deployment.
func parseImageTarManifest(data []byte) (string, string, error) {
	reader := tar.NewReader(bytes.NewReader(data))
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return "", "", errors.New("no manifest.json found; is this a docker save tar?")
		}
		if err != nil {
			return "", "", err
		}
		if path.Clean(header.Name) != "manifest.json" {
			continue
		}
		var entries []imageTarManifestEntry
		if err := json.NewDecoder(reader).Decode(&entries); err != nil {
			return "", "", fmt.Errorf("parse manifest.json: %w", err)
		}
		if len(entries) != 1 {
			return "", "", fmt.Errorf("the tar contains %d images; save exactly one", len(entries))
		}
		if len(entries[0].RepoTags) == 0 {
			return "", "", errors.New("the tar carries no repo tag; create it with docker save <image>:<tag>")
		}
		return entries[0].RepoTags[0], imageIDFromConfig(entries[0].Config), nil
	}
}

// imageIDFromConfig maps the manifest Config entry (either <hex>.json or
// blobs/sha256/<hex>) onto an image id; unrecognized shapes yield an empty
// id, which only costs mutable-tag change detection.
func imageIDFromConfig(config string) string {
	hex := strings.TrimSuffix(path.Base(config), ".json")
	if len(hex) != 64 {
		return ""
	}
	for _, r := range hex {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return ""
		}
	}
	return "sha256:" + hex
}
