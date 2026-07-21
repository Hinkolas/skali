// Package registrytoken implements the signing side of the Docker registry
// token protocol (the auth.token mode of CNCF Distribution). skalid mints
// short-lived ES256 JWTs scoped to explicit repository actions; the
// registry verifies them offline against the signing certificate the
// installer generated at init, so no registry request ever calls skalid.
// The JWT assembly is hand-rolled on the standard library because signing
// is the easy direction of JOSE and the module deliberately carries no JWT
// dependency.
package registrytoken

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
)

const (
	// Service is the registry service name: the aud claim of every token
	// and the service the registry advertises in its 401 challenge.
	Service = "skali-registry"
	// Issuer is the iss claim and the issuer the registry config trusts.
	Issuer = "skalid"
	// NodeUser is the Basic username containerd presents from
	// registries.yaml; it is granted pull on any repository and nothing
	// else.
	NodeUser = "skali-node"
	// TokenTTL bounds every minted token. Clients re-challenge and fetch a
	// fresh token when one expires mid-session.
	TokenTTL = 15 * time.Minute
)

// Access is one granted repository scope, mirroring the access claim shape
// Distribution's token package decodes.
type Access struct {
	Type    string   `json:"type"`
	Name    string   `json:"name"`
	Actions []string `json:"actions"`
}

// GenerateSigningKeypair creates the ECDSA P-256 signing key and the
// self-signed certificate the registry trusts as its rootcertbundle. The
// certificate is deliberately long-lived: rotation is an explicit later
// operation, not something that may silently break pulls.
func GenerateSigningKeypair() (keyPEM, certPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate signing key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, fmt.Errorf("generate certificate serial: %w", err)
	}
	now := time.Now()
	template := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "skalid registry token signer"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(100, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, fmt.Errorf("create signing certificate: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("encode signing key: %w", err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	return keyPEM, certPEM, nil
}

// Signer mints registry tokens with one loaded signing key.
type Signer struct {
	key   *ecdsa.PrivateKey
	keyID string
}

// LoadSigner parses the PEM signing key and precomputes the key ID. The
// certificate is not needed here: the registry derives the same key ID
// from the certificates in its rootcertbundle.
func LoadSigner(keyPEM []byte) (*Signer, error) {
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, errors.New("registrytoken: signing key is not PEM")
	}
	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("registrytoken: parse signing key: %w", err)
	}
	keyID, err := deriveKeyID(&key.PublicKey)
	if err != nil {
		return nil, err
	}
	return &Signer{key: key, keyID: keyID}, nil
}

// KeyID is the libtrust-style key identifier Distribution matches the JWT
// kid header against.
func (s *Signer) KeyID() string { return s.keyID }

// deriveKeyID reproduces libtrust's key ID: the first 240 bits of the
// SHA-256 over the PKIX DER public key, base32 without padding, grouped in
// fours with colons. Distribution indexes its rootcertbundle keys by this
// exact string.
func deriveKeyID(public *ecdsa.PublicKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(public)
	if err != nil {
		return "", fmt.Errorf("registrytoken: encode public key: %w", err)
	}
	digest := sha256.Sum256(der)
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(digest[:30])
	groups := make([]string, 0, len(encoded)/4)
	for start := 0; start < len(encoded); start += 4 {
		groups = append(groups, encoded[start:start+4])
	}
	return strings.Join(groups, ":"), nil
}

// claims is the registry token claim set. aud is a plain string because
// Distribution decodes a single audience.
type claims struct {
	Issuer    string   `json:"iss"`
	Subject   string   `json:"sub"`
	Audience  string   `json:"aud"`
	ExpiresAt int64    `json:"exp"`
	NotBefore int64    `json:"nbf"`
	IssuedAt  int64    `json:"iat"`
	JWTID     string   `json:"jti"`
	Access    []Access `json:"access"`
}

// Mint signs one token. The access slice states exactly what the token
// authorizes; an empty slice is a valid authentication-only token (docker
// login probes with no scope).
func (s *Signer) Mint(service, subject string, access []Access, now time.Time, ttl time.Duration) (string, error) {
	jti := make([]byte, 16)
	if _, err := rand.Read(jti); err != nil {
		return "", fmt.Errorf("registrytoken: generate token id: %w", err)
	}
	if access == nil {
		access = []Access{}
	}
	header, err := json.Marshal(map[string]string{
		"typ": "JWT",
		"alg": "ES256",
		"kid": s.keyID,
	})
	if err != nil {
		return "", fmt.Errorf("registrytoken: encode header: %w", err)
	}
	body, err := json.Marshal(claims{
		Issuer:    Issuer,
		Subject:   subject,
		Audience:  service,
		ExpiresAt: now.Add(ttl).Unix(),
		NotBefore: now.Add(-time.Minute).Unix(),
		IssuedAt:  now.Unix(),
		JWTID:     hex.EncodeToString(jti),
		Access:    access,
	})
	if err != nil {
		return "", fmt.Errorf("registrytoken: encode claims: %w", err)
	}
	signing := base64.RawURLEncoding.EncodeToString(header) + "." +
		base64.RawURLEncoding.EncodeToString(body)
	digest := sha256.Sum256([]byte(signing))
	r, sig, err := ecdsa.Sign(rand.Reader, s.key, digest[:])
	if err != nil {
		return "", fmt.Errorf("registrytoken: sign token: %w", err)
	}
	// JWS ES256 signatures are the raw R and S values, each left-padded to
	// the 32-byte curve size, not the ASN.1 form crypto/ecdsa produces by
	// default.
	signature := make([]byte, 64)
	r.FillBytes(signature[:32])
	sig.FillBytes(signature[32:])
	return signing + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}
