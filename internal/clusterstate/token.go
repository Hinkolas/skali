package clusterstate

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Hinkolas/skali/internal/utils"
)

const TokenPrefix = "skali."

type Token struct {
	Version    int    `json:"version"`
	Invitation string `json:"invitation"`
	Credential string `json:"credential"`
	CAPin      string `json:"caPin"`
}

func NewToken(invitation, caPin string) (encoded string, token Token, err error) {
	if invitation == "" || caPin == "" {
		return "", Token{}, errors.New("invitation and CA pin are required")
	}
	if err := validateCAPin(caPin); err != nil {
		return "", Token{}, err
	}
	credential, err := utils.RandomToken(32)
	if err != nil {
		return "", Token{}, fmt.Errorf("generate invitation credential: %w", err)
	}
	token = Token{
		Version: CurrentVersion, Invitation: invitation,
		Credential: credential, CAPin: caPin,
	}
	body, err := json.Marshal(token)
	if err != nil {
		return "", Token{}, err
	}
	return TokenPrefix + base64.RawURLEncoding.EncodeToString(body), token, nil
}

func ParseToken(value string) (Token, error) {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, TokenPrefix) {
		return Token{}, errors.New("not a reconciled Skali enrollment token")
	}
	body, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(value, TokenPrefix))
	if err != nil {
		return Token{}, fmt.Errorf("malformed Skali enrollment token: %w", err)
	}
	var token Token
	if err := json.Unmarshal(body, &token); err != nil {
		return Token{}, fmt.Errorf("malformed Skali enrollment token: %w", err)
	}
	if token.Version != CurrentVersion {
		return Token{}, fmt.Errorf("unsupported enrollment token version %d", token.Version)
	}
	if token.Invitation == "" || token.Credential == "" || token.CAPin == "" {
		return Token{}, errors.New("malformed Skali enrollment token: missing authentication fields")
	}
	if err := validateCAPin(token.CAPin); err != nil {
		return Token{}, fmt.Errorf("malformed Skali enrollment token: %w", err)
	}
	if _, err := base64.RawURLEncoding.DecodeString(token.Credential); err != nil {
		return Token{}, errors.New("malformed Skali enrollment token: invalid credential")
	}
	return token, nil
}

func validateCAPin(value string) error {
	digest, ok := strings.CutPrefix(value, "sha256:")
	if !ok {
		return errors.New("coordinator CA pin must use sha256")
	}
	decoded, err := hex.DecodeString(digest)
	if err != nil || len(decoded) != sha256.Size {
		return errors.New("coordinator CA pin must contain a 32-byte SHA-256 digest")
	}
	return nil
}

func CredentialHash(credential string) string {
	sum := sha256.Sum256([]byte(credential))
	return hex.EncodeToString(sum[:])
}

func MatchCredential(hash, credential string) bool {
	expected, err := hex.DecodeString(hash)
	if err != nil {
		return false
	}
	sum := sha256.Sum256([]byte(credential))
	return subtle.ConstantTimeCompare(expected, sum[:]) == 1
}
