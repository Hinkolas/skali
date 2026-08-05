package backup

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/Hinkolas/skali/internal/utils"
	"github.com/klauspost/compress/zstd"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// The worker is the in-Job half of the backup engine: `skalid
// backup-worker <upload|download|tar|untar>` runs inside a Kubernetes Job
// with the S3 target injected as SKALI_BACKUP_* environment variables from
// the per-operation Secret. It moves data between the pod filesystem and
// the target so bulk bytes never transit the daemon or the Kubernetes API
// server.

// sha256MetadataKey stores an object's content hash as S3 user metadata;
// downloads verify it when present.
const sha256MetadataKey = "sha256"

// RunWorker dispatches one worker subcommand; it is the whole process.
func RunWorker(args []string) error {
	if len(args) == 0 {
		return errors.New("backup-worker: usage: backup-worker <upload|download|tar|untar> [flags]")
	}
	ctx := context.Background()
	switch args[0] {
	case "upload":
		return workerUpload(ctx, args[1:])
	case "download":
		return workerDownload(ctx, args[1:])
	case "tar":
		return workerTar(ctx, args[1:])
	case "untar":
		return workerUntar(ctx, args[1:])
	default:
		return fmt.Errorf("backup-worker: unknown subcommand %q", args[0])
	}
}

// workerClient opens the S3 target from the injected environment.
func workerClient() (*minio.Client, string, error) {
	endpoint := os.Getenv("SKALI_BACKUP_ENDPOINT")
	bucket := os.Getenv("SKALI_BACKUP_BUCKET")
	if endpoint == "" || bucket == "" {
		return nil, "", errors.New("backup-worker: SKALI_BACKUP_ENDPOINT and SKALI_BACKUP_BUCKET are required")
	}
	secure := false
	switch {
	case strings.HasPrefix(endpoint, "https://"):
		endpoint, secure = strings.TrimPrefix(endpoint, "https://"), true
	case strings.HasPrefix(endpoint, "http://"):
		endpoint = strings.TrimPrefix(endpoint, "http://")
	default:
		return nil, "", fmt.Errorf("backup-worker: endpoint %q must be an http or https URL", endpoint)
	}
	client, err := minio.New(strings.TrimSuffix(endpoint, "/"), &minio.Options{
		Creds: credentials.NewStaticV4(os.Getenv("SKALI_BACKUP_ACCESS_KEY_ID"),
			os.Getenv("SKALI_BACKUP_SECRET_ACCESS_KEY"), ""),
		Region:       os.Getenv("SKALI_BACKUP_REGION"),
		Secure:       secure,
		BucketLookup: minio.BucketLookupPath,
	})
	if err != nil {
		return nil, "", fmt.Errorf("backup-worker: create s3 client: %w", err)
	}
	return client, bucket, nil
}

// workerUpload streams one file to the target with its sha256 recorded as
// object metadata. The hash pass reads the file once before the upload
// pass; dumps sit on the Job's emptyDir, so two reads are cheap and the
// hash is known before PutObject needs it.
func workerUpload(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("upload", flag.ContinueOnError)
	file := flags.String("file", "", "file to upload")
	key := flags.String("key", "", "object key")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *file == "" || *key == "" {
		return errors.New("backup-worker: --file and --key are required")
	}
	client, bucket, err := workerClient()
	if err != nil {
		return err
	}
	digest, size, err := utils.FileSHA256(*file)
	if err != nil {
		return fmt.Errorf("backup-worker: %w", err)
	}
	reader, err := os.Open(*file)
	if err != nil {
		return fmt.Errorf("backup-worker: open %s: %w", *file, err)
	}
	defer reader.Close()
	_, err = client.PutObject(ctx, bucket, *key, reader, size, minio.PutObjectOptions{
		UserMetadata: map[string]string{sha256MetadataKey: digest},
	})
	if err != nil {
		return fmt.Errorf("backup-worker: upload %s: %w", *key, err)
	}
	slog.Info("uploaded", "key", *key, "bytes", size, "sha256", digest)
	return nil
}

// workerDownload fetches one object to a file and verifies its recorded
// sha256 when present.
func workerDownload(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("download", flag.ContinueOnError)
	key := flags.String("key", "", "object key")
	file := flags.String("file", "", "destination file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *file == "" || *key == "" {
		return errors.New("backup-worker: --key and --file are required")
	}
	client, bucket, err := workerClient()
	if err != nil {
		return err
	}
	stat, err := client.StatObject(ctx, bucket, *key, minio.StatObjectOptions{})
	if err != nil {
		return fmt.Errorf("backup-worker: stat %s: %w", *key, err)
	}
	object, err := client.GetObject(ctx, bucket, *key, minio.GetObjectOptions{})
	if err != nil {
		return fmt.Errorf("backup-worker: get %s: %w", *key, err)
	}
	defer object.Close()
	destination, err := os.Create(*file)
	if err != nil {
		return fmt.Errorf("backup-worker: create %s: %w", *file, err)
	}
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(destination, hash), object)
	if closeErr := destination.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("backup-worker: download %s: %w", *key, err)
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	if want := stat.UserMetadata["Sha256"]; want != "" && want != digest {
		return fmt.Errorf("backup-worker: %s sha256 mismatch: stored %s, downloaded %s", *key, want, digest)
	}
	slog.Info("downloaded", "key", *key, "bytes", written, "sha256", digest)
	return nil
}

// workerTar streams a zstd-compressed tar of the mounted path to the
// target. The stream pipes straight into a multipart upload: nothing is
// staged on disk, so the volume can exceed the node's free space.
func workerTar(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("tar", flag.ContinueOnError)
	root := flags.String("path", "", "directory to archive")
	key := flags.String("key", "", "object key")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *root == "" || *key == "" {
		return errors.New("backup-worker: --path and --key are required")
	}
	client, bucket, err := workerClient()
	if err != nil {
		return err
	}
	reader, writer := io.Pipe()
	go func() {
		writer.CloseWithError(writeTar(*root, writer))
	}()
	_, err = client.PutObject(ctx, bucket, *key, reader, -1, minio.PutObjectOptions{})
	if err != nil {
		return fmt.Errorf("backup-worker: upload %s: %w", *key, err)
	}
	slog.Info("archived", "path", *root, "key", *key)
	return nil
}

func writeTar(root string, out io.Writer) error {
	compressor, err := zstd.NewWriter(out)
	if err != nil {
		return fmt.Errorf("backup-worker: create compressor: %w", err)
	}
	archive := tar.NewWriter(compressor)
	err = filepath.WalkDir(root, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, current)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		if relative == "lost+found" {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		var link string
		if info.Mode()&fs.ModeSymlink != 0 {
			if link, err = os.Readlink(current); err != nil {
				return err
			}
		}
		header, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(relative)
		if info.IsDir() {
			header.Name += "/"
		}
		if err := archive.WriteHeader(header); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		file, err := os.Open(current)
		if err != nil {
			return err
		}
		defer file.Close()
		_, err = io.Copy(archive, file)
		return err
	})
	if err != nil {
		return fmt.Errorf("backup-worker: archive %s: %w", root, err)
	}
	if err := archive.Close(); err != nil {
		return fmt.Errorf("backup-worker: finish archive: %w", err)
	}
	if err := compressor.Close(); err != nil {
		return fmt.Errorf("backup-worker: finish compression: %w", err)
	}
	return nil
}

// workerUntar restores an archive into the mounted path, clearing previous
// contents first so the volume matches the snapshot exactly.
func workerUntar(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("untar", flag.ContinueOnError)
	key := flags.String("key", "", "object key")
	root := flags.String("path", "", "destination directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *root == "" || *key == "" {
		return errors.New("backup-worker: --key and --path are required")
	}
	client, bucket, err := workerClient()
	if err != nil {
		return err
	}
	object, err := client.GetObject(ctx, bucket, *key, minio.GetObjectOptions{})
	if err != nil {
		return fmt.Errorf("backup-worker: get %s: %w", *key, err)
	}
	defer object.Close()
	if err := clearDirectory(*root); err != nil {
		return err
	}
	if err := extractTar(*root, object); err != nil {
		return err
	}
	slog.Info("restored", "key", *key, "path", *root)
	return nil
}

// clearDirectory removes everything under root except lost+found, which
// belongs to the filesystem, not the data.
func clearDirectory(root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("backup-worker: read %s: %w", root, err)
	}
	for _, entry := range entries {
		if entry.Name() == "lost+found" {
			continue
		}
		if err := os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
			return fmt.Errorf("backup-worker: clear %s: %w", root, err)
		}
	}
	return nil
}

func extractTar(root string, in io.Reader) error {
	decompressor, err := zstd.NewReader(in)
	if err != nil {
		return fmt.Errorf("backup-worker: create decompressor: %w", err)
	}
	defer decompressor.Close()
	archive := tar.NewReader(decompressor)
	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("backup-worker: read archive: %w", err)
		}
		target, err := securePath(root, header.Name)
		if err != nil {
			return err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, fs.FileMode(header.Mode)&fs.ModePerm); err != nil {
				return fmt.Errorf("backup-worker: create directory %s: %w", target, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("backup-worker: create parent of %s: %w", target, err)
			}
			file, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, fs.FileMode(header.Mode)&fs.ModePerm)
			if err != nil {
				return fmt.Errorf("backup-worker: create %s: %w", target, err)
			}
			_, err = io.Copy(file, archive)
			if closeErr := file.Close(); err == nil {
				err = closeErr
			}
			if err != nil {
				return fmt.Errorf("backup-worker: write %s: %w", target, err)
			}
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("backup-worker: create parent of %s: %w", target, err)
			}
			if err := os.Symlink(header.Linkname, target); err != nil {
				return fmt.Errorf("backup-worker: create symlink %s: %w", target, err)
			}
		default:
			slog.Warn("skipping unsupported archive entry", "name", header.Name, "type", header.Typeflag)
		}
	}
}

// securePath refuses archive entries that would escape the destination.
func securePath(root, name string) (string, error) {
	cleaned := filepath.Join(root, filepath.FromSlash(name))
	if cleaned != root && !strings.HasPrefix(cleaned, root+string(os.PathSeparator)) {
		return "", fmt.Errorf("backup-worker: archive entry %q escapes the destination", name)
	}
	return cleaned, nil
}
