package seaweed

import (
	"crypto/rand"
	"encoding/json"
	"math/big"
)

func randomString(alphabet string, length int) string {
	b := make([]byte, length)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			// crypto/rand failing is not a recoverable state.
			panic(err)
		}
		b[i] = alphabet[n.Int64()]
	}
	return string(b)
}

// GenerateAccessKey returns a fresh access key id, AWS-shaped (20 chars,
// upper-case alphanumeric) so every SDK's validation is happy.
func GenerateAccessKey() string {
	return randomString("ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789", 20)
}

// GenerateSecretKey returns a fresh secret key (40 chars, URL-safe so
// assembled URLs never need percent-encoding, the same rule as database
// passwords). Generated only at create and rotation, written straight into
// the Kubernetes Secret; never a row, never a revision.
func GenerateSecretKey() string {
	return randomString("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789", 40)
}

// BucketActions scopes an identity to exactly its bucket.
func BucketActions(bucket string) []string {
	return []string{
		"Read:" + bucket,
		"Write:" + bucket,
		"List:" + bucket,
		"Tagging:" + bucket,
	}
}

// PlatformIdentityName is Skali's own S3 principal: the one identity with
// the Admin action, used for what tenants must not do themselves (bucket
// configuration, restore writes, authenticated readiness). Its keypair
// lives in a platform Secret and is never mirrored to an environment.
const PlatformIdentityName = "skali-platform"

// PlatformActions is the platform identity's action set: the legacy Admin
// action grants every operation on every bucket.
func PlatformActions() []string {
	return []string{"Admin"}
}

// PrincipalARN is the ARN the engine evaluates bucket policies against for
// a legacy identity (measured on the pin: the default account id and the
// identity's name).
func PrincipalARN(identity string) string {
	return "arn:aws:iam::000000000000:user/" + identity
}

// bucketAdministrationActions are the granular actions the engine resolves
// bucket-configuration requests to (weed/s3api/s3_action_resolver.go on the
// pin). The legacy Write action the bucket identity holds would otherwise
// admit every one of them: policy, CORS, lifecycle, versioning, ACLs,
// tagging, notifications, object lock.
var bucketAdministrationActions = []string{
	"s3:PutBucketPolicy",
	"s3:DeleteBucketPolicy",
	"s3:PutBucketCors",
	"s3:DeleteBucketCors",
	"s3:PutLifecycleConfiguration",
	"s3:PutBucketVersioning",
	"s3:PutBucketAcl",
	"s3:PutObjectAcl",
	"s3:PutBucketTagging",
	"s3:DeleteBucketTagging",
	"s3:PutBucketNotification",
	"s3:PutBucketObjectLockConfiguration",
}

// BucketPolicy is the policy document Skali owns on every bucket: an
// explicit Deny of bucket administration for the bucket's own identity.
// The engine evaluates bucket policies ahead of identity actions and an
// explicit Deny wins, which is what separates object access (the identity's
// Write) from the settings Skali reconciles. The policy names the
// identity's principal, so the platform identity keeps administering.
func BucketPolicy(bucket string) string {
	document := map[string]any{
		"Version": "2012-10-17",
		"Statement": []map[string]any{{
			"Sid":       "SkaliOwnsBucketConfiguration",
			"Effect":    "Deny",
			"Principal": map[string]any{"AWS": PrincipalARN(bucket)},
			"Action":    bucketAdministrationActions,
			"Resource":  []string{"arn:aws:s3:::" + bucket, "arn:aws:s3:::" + bucket + "/*"},
		}},
	}
	data, err := json.Marshal(document)
	if err != nil {
		panic(err) // a fixed document; cannot fail
	}
	return string(data)
}
