package installer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/Hinkolas/skali/internal/installer/host"
	"github.com/Hinkolas/skali/internal/layout"
)

const joinPreflightTimeout = 10 * time.Second

var bootstrapTokenPattern = regexp.MustCompile(`^[a-z0-9]{6}\.[a-z0-9]{16}$`)

// JoinTokenClaims are the non-secret routing claims carried by a composite
// token. Empty fields identify an older token whose values must still be
// supplied by flags, config, or prompts.
type JoinTokenClaims struct {
	Cluster string
	Server  string
	Role    string
}

// InspectJoinToken returns routing claims without exposing the embedded
// k3s or registry credentials.
func InspectJoinToken(token string) (JoinTokenClaims, error) {
	decoded, err := decodeJoinTokenClaims(strings.TrimSpace(token))
	if err != nil {
		return JoinTokenClaims{}, err
	}
	return JoinTokenClaims{
		Cluster: decoded.Cluster,
		Server:  decoded.Server,
		Role:    decoded.Role,
	}, nil
}

type resolvedInstall struct {
	Cluster      string
	Role         string
	Capabilities []string
	NodeName     string
	NodeIP       string
	Server       string
	K3sToken     string
	PullSecret   string
}

func resolveAndPreflightInstall(ctx context.Context, runner host.Runner, detected *Host, opts InstallOptions) (resolvedInstall, error) {
	resolved := resolvedInstall{
		Cluster:      opts.Cluster,
		Role:         opts.Role,
		Capabilities: append([]string(nil), opts.Capabilities...),
		NodeName:     detected.Hostname,
		NodeIP:       opts.NodeIP,
	}

	var decoded decodedJoinToken
	if opts.Join != nil {
		token := strings.TrimSpace(opts.Join.Token)
		if token == "" {
			data, err := runner.ReadFile(ctx, opts.Join.TokenFile)
			if err != nil {
				return resolved, fmt.Errorf("read join token file %s: %w", opts.Join.TokenFile, err)
			}
			token = strings.TrimSpace(string(data))
			if token == "" {
				return resolved, fmt.Errorf("join token file %s is empty", opts.Join.TokenFile)
			}
		}
		var err error
		decoded, err = decodeJoinTokenClaims(token)
		if err != nil {
			return resolved, err
		}
		if resolved.Role == "" {
			resolved.Role = decoded.Role
		}
		if resolved.Cluster == "" {
			resolved.Cluster = decoded.Cluster
		}
		resolved.Server = strings.TrimSpace(opts.Join.Server)
		if resolved.Server == "" {
			resolved.Server = decoded.Server
		}
		resolved.K3sToken = decoded.K3s
		resolved.PullSecret = decoded.Pull
		if decoded.Role != "" && resolved.Role != decoded.Role {
			return resolved, fmt.Errorf("this join token was minted for role %s, not %s; "+
				"mint a matching token with skali cluster token --role %s",
				decoded.Role, resolved.Role, resolved.Role)
		}
		if decoded.Cluster != "" && resolved.Cluster != decoded.Cluster {
			return resolved, fmt.Errorf("this join token belongs to cluster %q, not %q",
				decoded.Cluster, resolved.Cluster)
		}
		if resolved.Role == "" {
			return resolved, errors.New("this older join token has no role claim; supply role explicitly")
		}
		if resolved.Cluster == "" {
			return resolved, errors.New("this older join token has no cluster claim; supply cluster explicitly")
		}
	}

	if resolved.Role == "" {
		resolved.Role = layout.RoleServer
	}
	if resolved.Cluster == "" {
		resolved.Cluster = DefaultCluster
	}
	if err := validateResolvedInstall(ctx, runner, resolved, opts.Join != nil); err != nil {
		return resolved, err
	}
	if opts.Join != nil {
		if err := preflightJoin(ctx, runner, resolved.Server, resolved.K3sToken, resolved.Role); err != nil {
			return resolved, fmt.Errorf("join preflight for %s failed: %w",
				resolved.Server, err)
		}
	}
	return resolved, nil
}

func validateResolvedInstall(ctx context.Context, runner host.Runner, resolved resolvedInstall, joining bool) error {
	if resolved.Role != layout.RoleServer && resolved.Role != layout.RoleAgent {
		return fmt.Errorf("role must be server or agent, got %q", resolved.Role)
	}
	if resolved.Role == layout.RoleAgent && !joining {
		return errors.New("role agent requires join options pointing at an existing server")
	}
	if problems := validation.IsValidLabelValue(resolved.Cluster); len(problems) > 0 {
		return fmt.Errorf("cluster name %q is not a valid Kubernetes label value: %s",
			resolved.Cluster, strings.Join(problems, "; "))
	}
	if len(resolved.Capabilities) == 0 {
		return errors.New("at least one capability is required")
	}
	for _, capability := range resolved.Capabilities {
		if !slices.Contains(layout.Capabilities, capability) {
			return fmt.Errorf("unknown capability %q; expected one of %s",
				capability, strings.Join(layout.Capabilities, ", "))
		}
	}
	if strings.TrimSpace(resolved.NodeName) == "" {
		return errors.New("could not determine the hostname for node naming")
	}
	if problems := validation.IsDNS1123Subdomain(resolved.NodeName); len(problems) > 0 {
		return fmt.Errorf("hostname %q is not a valid Kubernetes node name: %s",
			resolved.NodeName, strings.Join(problems, "; "))
	}
	if resolved.NodeIP != "" {
		if net.ParseIP(resolved.NodeIP) == nil {
			return fmt.Errorf("node IP %q is not a valid IP address", resolved.NodeIP)
		}
		result, err := runner.Run(ctx, host.Command{Name: "ip", Args: []string{"-o", "addr", "show"}})
		if err != nil {
			return fmt.Errorf("verify node IP %s: %w", resolved.NodeIP, err)
		}
		if result.ExitCode != 0 {
			return fmt.Errorf("verify node IP %s: ip exited %d: %s",
				resolved.NodeIP, result.ExitCode, strings.TrimSpace(result.Stderr))
		}
		found := false
		for _, field := range strings.Fields(result.Stdout) {
			address, _, _ := strings.Cut(field, "/")
			if address == resolved.NodeIP {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("node IP %s is not assigned to this host", resolved.NodeIP)
		}
	}
	if joining {
		if resolved.Server == "" {
			return errors.New("joining requires a server URL (use a current Skali token or --server)")
		}
		if resolved.K3sToken == "" {
			return errors.New("joining requires a token or a token file")
		}
		if _, err := normalizeJoinServer(resolved.Server); err != nil {
			return err
		}
	}
	return nil
}

func validateInstallMetadata(opts InstallOptions) error {
	if opts.Endpoints != nil {
		if opts.Endpoints.API != "" && !validDomain(opts.Endpoints.API) {
			return fmt.Errorf("api/ui domain %q is not a valid DNS name", opts.Endpoints.API)
		}
		if opts.Endpoints.Registry != "" && !validDomain(opts.Endpoints.Registry) {
			return fmt.Errorf("registry domain %q is not a valid DNS name", opts.Endpoints.Registry)
		}
	}
	if opts.TLS != nil && opts.TLS.IssuerEmail != "" {
		address, err := mail.ParseAddress(opts.TLS.IssuerEmail)
		if err != nil || address.Address != opts.TLS.IssuerEmail {
			return fmt.Errorf("tls issuer email %q is not a valid email address", opts.TLS.IssuerEmail)
		}
	}
	return nil
}

func validDomain(value string) bool {
	if len(value) == 0 || len(value) > 253 || strings.HasPrefix(value, ".") || strings.HasSuffix(value, ".") {
		return false
	}
	for label := range strings.SplitSeq(value, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if (character < 'a' || character > 'z') &&
				(character < 'A' || character > 'Z') &&
				(character < '0' || character > '9') && character != '-' {
				return false
			}
		}
	}
	return true
}

func normalizeJoinServer(value string) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil {
		return "", fmt.Errorf("join server %q is invalid: %w", value, err)
	}
	if parsed.Scheme != "https" || parsed.Host == "" {
		return "", fmt.Errorf("join server must be an https:// URL, got %q", value)
	}
	if parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("join server must be an HTTPS origin without credentials, path, query, or fragment, got %q", value)
	}
	return strings.TrimSuffix(parsed.String(), "/"), nil
}

// NormalizeJoinServer validates and canonicalizes a public join endpoint.
func NormalizeJoinServer(value string) (string, error) {
	return normalizeJoinServer(value)
}

func preflightJoin(ctx context.Context, runner host.Runner, server, token, role string) error {
	server, err := normalizeJoinServer(server)
	if err != nil {
		return err
	}
	parsed, err := parseSecureK3sToken(token)
	if err != nil {
		return err
	}

	cacerts, err := runner.ProbeHTTP(ctx, host.HTTPRequest{
		URL:      server + "/cacerts",
		Insecure: true,
		Timeout:  joinPreflightTimeout,
	})
	if err != nil {
		return classifyProbeError(err)
	}
	if cacerts.StatusCode < 200 || cacerts.StatusCode >= 300 {
		return fmt.Errorf("/cacerts returned HTTP %d", cacerts.StatusCode)
	}
	actualHash, err := hashK3sCA(cacerts.Body)
	if err != nil {
		return fmt.Errorf("server returned a malformed CA bundle: %w", err)
	}
	if !strings.EqualFold(parsed.CAHash, actualHash) {
		return fmt.Errorf("token CA hash does not match the server (%s != %s)", parsed.CAHash, actualHash)
	}

	path := "/v1-k3s/config"
	if role == layout.RoleServer {
		path = "/v1-k3s/server-bootstrap"
	}
	request := host.HTTPRequest{
		URL:     server + path,
		CACerts: cacerts.Body,
		Timeout: joinPreflightTimeout,
	}
	if parsed.Bearer != "" {
		request.Bearer = parsed.Bearer
	} else {
		request.Username = parsed.Username
		request.Password = parsed.Password
	}
	response, err := runner.ProbeHTTP(ctx, request)
	if err != nil {
		return fmt.Errorf("TLS or authenticated endpoint check failed: %w", classifyProbeError(err))
	}
	switch response.StatusCode {
	case 200:
		return nil
	case 401:
		return errors.New("the join credential is invalid or expired (HTTP 401)")
	case 403:
		return fmt.Errorf("the join credential is not authorized for role %s; it may be a wrong-role token (HTTP 403)", role)
	default:
		return fmt.Errorf("the authenticated %s endpoint returned HTTP %d", role, response.StatusCode)
	}
}

type secureK3sToken struct {
	CAHash   string
	Username string
	Password string
	Bearer   string
}

func parseSecureK3sToken(token string) (secureK3sToken, error) {
	if !strings.HasPrefix(token, "K10") {
		return secureK3sToken{}, errors.New("join token is not in secure K10 format; mint a fresh token on a server")
	}
	parts := strings.SplitN(strings.TrimPrefix(token, "K10"), "::", 2)
	if len(parts) != 2 || len(parts[0]) != sha256.Size*2 {
		return secureK3sToken{}, errors.New("secure join token has an invalid CA hash")
	}
	if _, err := hex.DecodeString(parts[0]); err != nil {
		return secureK3sToken{}, errors.New("secure join token CA hash is not hexadecimal")
	}
	if strings.ContainsAny(parts[1], "\r\n") {
		return secureK3sToken{}, errors.New("secure join token credentials contain a line break")
	}
	parsed := secureK3sToken{CAHash: strings.ToLower(parts[0])}
	if bootstrapTokenPattern.MatchString(parts[1]) {
		parsed.Bearer = parts[1]
		return parsed, nil
	}
	parsed.Username, parsed.Password, _ = strings.Cut(parts[1], ":")
	if parsed.Username == "" || parsed.Password == "" {
		return secureK3sToken{}, errors.New("secure join token has invalid credentials")
	}
	return parsed, nil
}

func hashK3sCA(data []byte) (string, error) {
	var certificates []*x509.Certificate
	rest := data
	for {
		block, remaining := pem.Decode(rest)
		if block == nil {
			break
		}
		rest = remaining
		if block.Type != "CERTIFICATE" {
			continue
		}
		certificate, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return "", err
		}
		certificates = append(certificates, certificate)
	}
	if len(certificates) == 0 {
		return "", errors.New("no certificates found")
	}
	hashInput := data
	if len(certificates) > 1 {
		roots := x509.NewCertPool()
		intermediates := x509.NewCertPool()
		for index, certificate := range certificates {
			if index == 0 {
				continue
			}
			if len(certificate.AuthorityKeyId) == 0 ||
				bytes.Equal(certificate.AuthorityKeyId, certificate.SubjectKeyId) {
				roots.AddCert(certificate)
			} else {
				intermediates.AddCert(certificate)
			}
		}
		if chains, err := certificates[0].Verify(x509.VerifyOptions{
			Roots: roots, Intermediates: intermediates,
		}); err == nil && len(chains) > 0 {
			chain := chains[0]
			hashInput = chain[len(chain)-1].Raw
		}
	}
	digest := sha256.Sum256(hashInput)
	return hex.EncodeToString(digest[:]), nil
}

func classifyProbeError(err error) error {
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "no such host"):
		return fmt.Errorf("DNS lookup failed: %w", err)
	case strings.Contains(message, "connection refused"):
		return fmt.Errorf("connection was refused: %w", err)
	case strings.Contains(message, "timeout"), strings.Contains(message, "deadline exceeded"):
		return fmt.Errorf("connection timed out: %w", err)
	default:
		return err
	}
}
