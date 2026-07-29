package seaweed

import (
	"crypto/rand"
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
