package seaweed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// ErrNoPlatformCredentials reports that the platform identity's keypair
// has not been loaded into the client yet (the store's reconcile does
// that once the identity exists); callers on the observation path skip
// configuration work until then.
var ErrNoPlatformCredentials = errors.New("seaweed: platform identity credentials not loaded")

// SetPlatformCredentials installs the platform identity's keypair for the
// S3 admin path (bucket configuration); the substrate calls it after
// ensuring the identity.
func (c *Client) SetPlatformCredentials(accessKey, secretKey string) {
	c.platformMu.Lock()
	defer c.platformMu.Unlock()
	c.platformAccessKey, c.platformSecretKey = accessKey, secretKey
}

// s3 builds a client for the S3 API as the platform identity, bound to
// the gateway address the transport can dial (Service DNS in-cluster, a
// port-forward from a laptop). Unlike the filer and master calls this
// cannot ride the API server's service proxy: SigV4 covers the Host header
// and the path the proxy rewrites.
func (c *Client) s3(ctx context.Context) (*minio.Client, error) {
	c.platformMu.Lock()
	accessKey, secretKey := c.platformAccessKey, c.platformSecretKey
	c.platformMu.Unlock()
	if accessKey == "" {
		return nil, ErrNoPlatformCredentials
	}
	address, err := c.doer.ServiceAddress(ctx, c.namespace, S3Service, S3Port)
	if err != nil {
		return nil, err
	}
	client, err := minio.New(address, &minio.Options{
		Creds:        credentials.NewStaticV4(accessKey, secretKey, ""),
		Region:       Region,
		BucketLookup: minio.BucketLookupPath,
	})
	if err != nil {
		return nil, fmt.Errorf("seaweed: s3 client: %w", err)
	}
	return client, nil
}

// EnsureBucketConfiguration converges what Skali owns on a bucket: the
// policy document, no CORS configuration, no lifecycle rules, and
// versioning not enabled. Anything else found is drift, an application
// identity having changed a setting behind the platform (the policy itself
// denies that from now on), and is reset. It returns the settings it had
// to reset, empty when the bucket was already converged. Serialized with
// the other document writes so a probe and a provisioning pass never
// interleave.
func (c *Client) EnsureBucketConfiguration(ctx context.Context, bucket, policy string) ([]string, error) {
	c.configMu.Lock()
	defer c.configMu.Unlock()
	client, err := c.s3(ctx)
	if err != nil {
		return nil, err
	}
	var repaired []string
	fail := func(what string, err error) ([]string, error) {
		// A transport failure may be a dead forward; the next call
		// re-establishes it.
		if minio.ToErrorResponse(err).Code == "" {
			c.doer.ForgetServiceAddress(c.namespace, S3Service, S3Port)
		}
		return nil, fmt.Errorf("seaweed: %s %s: %w", what, bucket, err)
	}

	current, err := client.GetBucketPolicy(ctx, bucket)
	if err != nil {
		return fail("read bucket policy", err)
	}
	if !samePolicy(current, policy) {
		if err := client.SetBucketPolicy(ctx, bucket, policy); err != nil {
			return fail("set bucket policy", err)
		}
		repaired = append(repaired, "policy")
	}

	corsConfig, err := client.GetBucketCors(ctx, bucket)
	if err != nil && !absent(err) {
		return fail("read bucket cors", err)
	}
	if corsConfig != nil && len(corsConfig.CORSRules) > 0 {
		if err := client.SetBucketCors(ctx, bucket, nil); err != nil {
			return fail("delete bucket cors", err)
		}
		repaired = append(repaired, "cors")
	}

	lifecycleConfig, err := client.GetBucketLifecycle(ctx, bucket)
	if err != nil && !absent(err) {
		return fail("read bucket lifecycle", err)
	}
	if lifecycleConfig != nil && len(lifecycleConfig.Rules) > 0 {
		if err := client.SetBucketLifecycle(ctx, bucket, nil); err != nil {
			return fail("delete bucket lifecycle", err)
		}
		repaired = append(repaired, "lifecycle")
	}

	versioning, err := client.GetBucketVersioning(ctx, bucket)
	if err != nil {
		return fail("read bucket versioning", err)
	}
	if versioning.Status == "Enabled" {
		if err := client.SetBucketVersioning(ctx, bucket, minio.BucketVersioningConfiguration{Status: "Suspended"}); err != nil {
			return fail("suspend bucket versioning", err)
		}
		repaired = append(repaired, "versioning")
	}
	return repaired, nil
}

// absent recognises the engine's "no such configuration" answers, which
// are the converged state, not errors.
func absent(err error) bool {
	switch minio.ToErrorResponse(err).Code {
	case "NoSuchCORSConfiguration", "NoSuchLifecycleConfiguration", "NoSuchBucketPolicy", "NotFound":
		return true
	}
	return false
}

// samePolicy compares two policy documents structurally: the engine may
// re-serialize what it stored, and formatting is not drift.
func samePolicy(current, desired string) bool {
	if current == "" {
		return desired == ""
	}
	var a, b any
	if json.Unmarshal([]byte(current), &a) != nil || json.Unmarshal([]byte(desired), &b) != nil {
		return false
	}
	return reflect.DeepEqual(a, b)
}

// platformCredentials and the configuration mutex live on Client; declared
// here beside their use.
type platformCredentials struct {
	platformMu        sync.Mutex
	platformAccessKey string
	platformSecretKey string
	configMu          sync.Mutex
}
