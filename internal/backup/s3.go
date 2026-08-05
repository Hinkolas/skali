package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// objectStore is the narrow S3 surface the backup engine uses, against one
// bucket. Both the external target and the in-cluster SeaweedFS gateway
// satisfy it through minio-go; tests use a fake.
type objectStore interface {
	// Put streams one object; size may be -1 when unknown (multipart).
	Put(ctx context.Context, key string, r io.Reader, size int64) error
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	// Stat returns the object's size and recorded sha256, or errNotFound.
	Stat(ctx context.Context, key string) (objectStat, error)
	// List calls fn for every object under prefix; fn errors abort.
	List(ctx context.Context, prefix string, fn func(objectInfo) error) error
	Remove(ctx context.Context, key string) error
	// Reachable verifies the bucket exists and answers.
	Reachable(ctx context.Context) error
}

type objectInfo struct {
	Key  string
	Size int64
}

// objectStat is one object's metadata: the size and, when the writer
// recorded one, the content sha256.
type objectStat struct {
	Size   int64
	SHA256 string
}

var errNotFound = errors.New("backup: object not found")

// s3Location is everything needed to open one bucket-scoped store.
type s3Location struct {
	Endpoint  string
	Region    string
	Bucket    string
	AccessKey string
	SecretKey string
}

func targetLocation(c *Credentials) s3Location {
	return s3Location{
		Endpoint:  c.Endpoint,
		Region:    c.Region,
		Bucket:    c.Bucket,
		AccessKey: c.AccessKeyID,
		SecretKey: c.SecretAccessKey,
	}
}

// minioStore adapts minio-go to objectStore. Path-style addressing is
// forced: SeaweedFS and most self-hosted targets do not serve virtual-host
// buckets.
type minioStore struct {
	client *minio.Client
	bucket string
}

func newObjectStore(loc s3Location) (*minioStore, error) {
	endpoint := loc.Endpoint
	secure := false
	switch {
	case strings.HasPrefix(endpoint, "https://"):
		endpoint, secure = strings.TrimPrefix(endpoint, "https://"), true
	case strings.HasPrefix(endpoint, "http://"):
		endpoint = strings.TrimPrefix(endpoint, "http://")
	default:
		return nil, fmt.Errorf("backup: endpoint %q must be an http or https URL", loc.Endpoint)
	}
	client, err := minio.New(strings.TrimSuffix(endpoint, "/"), &minio.Options{
		Creds:        credentials.NewStaticV4(loc.AccessKey, loc.SecretKey, ""),
		Region:       loc.Region,
		Secure:       secure,
		BucketLookup: minio.BucketLookupPath,
	})
	if err != nil {
		return nil, fmt.Errorf("backup: create s3 client: %w", err)
	}
	return &minioStore{client: client, bucket: loc.Bucket}, nil
}

func (s *minioStore) Put(ctx context.Context, key string, r io.Reader, size int64) error {
	_, err := s.client.PutObject(ctx, s.bucket, key, r, size, minio.PutObjectOptions{})
	if err != nil {
		return fmt.Errorf("backup: put %s: %w", key, err)
	}
	return nil
}

func (s *minioStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	object, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("backup: get %s: %w", key, err)
	}
	// GetObject is lazy; surface missing objects on the first read instead
	// of a confusing decode error downstream.
	if _, err := object.Stat(); err != nil {
		_ = object.Close()
		if isNoSuchKey(err) {
			return nil, errNotFound
		}
		return nil, fmt.Errorf("backup: get %s: %w", key, err)
	}
	return object, nil
}

func (s *minioStore) Stat(ctx context.Context, key string) (objectStat, error) {
	info, err := s.client.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		if isNoSuchKey(err) {
			return objectStat{}, errNotFound
		}
		return objectStat{}, fmt.Errorf("backup: stat %s: %w", key, err)
	}
	return objectStat{Size: info.Size, SHA256: info.UserMetadata["Sha256"]}, nil
}

func (s *minioStore) List(ctx context.Context, prefix string, fn func(objectInfo) error) error {
	for object := range s.client.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{
		Prefix:    prefix,
		Recursive: true,
	}) {
		if object.Err != nil {
			return fmt.Errorf("backup: list %s: %w", prefix, object.Err)
		}
		if err := fn(objectInfo{Key: object.Key, Size: object.Size}); err != nil {
			return err
		}
	}
	return nil
}

func (s *minioStore) Remove(ctx context.Context, key string) error {
	if err := s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("backup: remove %s: %w", key, err)
	}
	return nil
}

func (s *minioStore) Reachable(ctx context.Context) error {
	exists, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return fmt.Errorf("backup: reach bucket %s: %w", s.bucket, err)
	}
	if !exists {
		return fmt.Errorf("backup: bucket %s does not exist on %s", s.bucket, s.client.EndpointURL())
	}
	return nil
}

func isNoSuchKey(err error) bool {
	var response minio.ErrorResponse
	return errors.As(err, &response) && (response.Code == "NoSuchKey" || response.StatusCode == 404)
}
