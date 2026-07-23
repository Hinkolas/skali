package installer

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/Hinkolas/skali/internal/registrytoken"
)

// joinTokenPrefix marks a composite skali join token: the k3s join token
// plus the cluster's registry pull credential, so an enrolling agent
// receives both through the one secret the operator already carries.
const joinTokenPrefix = "skali1."

// joinTokenPayload is the composite token body. Role carries the role the
// token was minted for so a mismatched join fails plainly instead of with
// a confusing k3s bootstrap error; raw (non-composite) tokens carry no
// claim.
type joinTokenPayload struct {
	K3s  string `json:"k3s"`
	Pull string `json:"pull,omitempty"`
	Role string `json:"role,omitempty"`
}

// newPullSecret generates the cluster's shared registry pull credential;
// the first server mints it at install and it lives only in
// registries.yaml (root, 0600) until init copies it into the cluster.
func newPullSecret() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate registry pull credential: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// encodeJoinToken wraps the k3s token, the pull credential, and the
// minted role into the composite form `skali cluster token` prints. An
// empty role means an agent token.
func encodeJoinToken(k3sToken, pullSecret, role string) string {
	payload, _ := json.Marshal(joinTokenPayload{K3s: k3sToken, Pull: pullSecret, Role: role})
	return joinTokenPrefix + base64.RawURLEncoding.EncodeToString(payload)
}

// decodeJoinToken accepts both token forms: a composite token yields its
// parts, a raw k3s token passes through with no pull credential and no
// role claim so a manually minted token still joins (with a visible
// warning about unauthenticated registry pulls).
func decodeJoinToken(token string) (k3sToken, pullSecret, role string, err error) {
	if !strings.HasPrefix(token, joinTokenPrefix) {
		return token, "", "", nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(token, joinTokenPrefix))
	if err != nil {
		return "", "", "", fmt.Errorf("malformed skali join token: %w", err)
	}
	var payload joinTokenPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "", "", "", fmt.Errorf("malformed skali join token: %w", err)
	}
	if payload.K3s == "" {
		return "", "", "", fmt.Errorf("malformed skali join token: no k3s token inside")
	}
	return payload.K3s, payload.Pull, payload.Role, nil
}

// registriesPullSecret extracts the node pull credential from a rendered
// registries.yaml; registries.yaml is the single host-side home of the
// secret, so both `token` and `init` read it back from there.
func registriesPullSecret(data []byte) string {
	var file struct {
		Configs map[string]struct {
			Auth struct {
				Username string `yaml:"username"`
				Password string `yaml:"password"`
			} `yaml:"auth"`
		} `yaml:"configs"`
	}
	if err := yaml.Unmarshal(data, &file); err != nil {
		return ""
	}
	for _, config := range file.Configs {
		if config.Auth.Username == registrytoken.NodeUser && config.Auth.Password != "" {
			return config.Auth.Password
		}
	}
	return ""
}
