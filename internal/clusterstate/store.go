package clusterstate

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"

	"github.com/google/uuid"

	"github.com/Hinkolas/skali/internal/layout"
)

const (
	annotationRole        = "skali.dev/role"
	annotationExpires     = "skali.dev/expires-at"
	annotationUsedBy      = "skali.dev/used-by"
	annotationRevoked     = "skali.dev/revoked"
	annotationAllowedCaps = "skali.dev/allowed-capabilities"
	annotationCSRHash     = "skali.dev/csr-sha256"

	credentialHashKey = "credential-hash"
	caCertKey         = "ca.crt"
	caPrivateKey      = "ca.key"
	serverCertKey     = "tls.crt"
	serverPrivateKey  = "tls.key"
)

var stateResource = schema.GroupResource{Group: "", Resource: "configmaps"}

// Store persists installer-owned state in Kubernetes built-ins so it works
// before CRD controllers or the Skali product database exist.
type Store struct {
	Client kubernetes.Interface
	Now    func() time.Time
}

type Invitation struct {
	ID                  string
	Role                string
	AllowedCapabilities []string
	ExpiresAt           time.Time
	UsedBy              string
	Revoked             bool
}

type Trust struct {
	CACert     []byte
	CAKey      []byte
	ServerCert []byte
	ServerKey  []byte
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC().Truncate(time.Second)
	}
	return time.Now().UTC().Truncate(time.Second)
}

func (s *Store) Bootstrap(ctx context.Context, state *State) (Trust, error) {
	if s.Client == nil {
		return Trust{}, errors.New("cluster state store has no Kubernetes client")
	}
	if state == nil {
		return Trust{}, errors.New("cluster state bootstrap requires a state")
	}
	_, err := s.Client.CoreV1().Namespaces().Get(ctx, Namespace, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = s.Client.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: Namespace,
				Labels: map[string]string{
					"app.kubernetes.io/managed-by": "skali-installer",
					"skali.dev/cluster-system":     "true",
				},
			},
		}, metav1.CreateOptions{})
	}
	if err != nil {
		return Trust{}, fmt.Errorf("ensure %s namespace: %w", Namespace, err)
	}

	trust, err := generateTrust(state.Cluster, s.now())
	if err != nil {
		return Trust{}, err
	}
	_, err = s.Client.CoreV1().Secrets(Namespace).Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: TrustSecretName},
		Type:       corev1.SecretTypeTLS,
		Data: map[string][]byte{
			caCertKey: caBytes(trust.CACert), caPrivateKey: trust.CAKey,
			serverCertKey: trust.ServerCert, serverPrivateKey: trust.ServerKey,
		},
	}, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		trust, err = s.Trust(ctx)
	}
	if err != nil {
		return Trust{}, fmt.Errorf("create coordinator trust: %w", err)
	}

	data, err := json.Marshal(state)
	if err != nil {
		return Trust{}, err
	}
	_, err = s.Client.CoreV1().ConfigMaps(Namespace).Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:   StateName,
			Labels: map[string]string{"skali.dev/cluster-state": "true"},
		},
		Data: map[string]string{StateKey: string(data)},
	}, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		existing, loadErr := s.Load(ctx)
		if loadErr != nil {
			return Trust{}, loadErr
		}
		if existing.Cluster != state.Cluster {
			return Trust{}, fmt.Errorf("coordinator state belongs to cluster %q, not %q",
				existing.Cluster, state.Cluster)
		}
		return trust, nil
	}
	if err != nil {
		return Trust{}, fmt.Errorf("create cluster state: %w", err)
	}
	return trust, nil
}

func (s *Store) Load(ctx context.Context) (*State, error) {
	configMap, err := s.Client.CoreV1().ConfigMaps(Namespace).
		Get(ctx, StateName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("read cluster state: %w", err)
	}
	state, err := decodeState(configMap.Data[StateKey])
	if err != nil {
		return nil, err
	}
	return state, nil
}

func decodeState(value string) (*State, error) {
	var state State
	if err := json.Unmarshal([]byte(value), &state); err != nil {
		return nil, fmt.Errorf("decode cluster state: %w", err)
	}
	if state.Version != CurrentVersion || state.Cluster == "" ||
		state.CandidateRevision == "" || state.ConvergedRevision == "" {
		return nil, errors.New("cluster state is missing required identity or revision fields")
	}
	if _, err := state.Candidate(); err != nil {
		return nil, err
	}
	if _, err := state.Converged(); err != nil {
		return nil, err
	}
	return &state, nil
}

// Update applies one compare-and-swap mutation. The callback can be retried
// and therefore must only mutate the provided in-memory State.
func (s *Store) Update(ctx context.Context, mutate func(*State) error) (*State, error) {
	var result *State
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		configMap, err := s.Client.CoreV1().ConfigMaps(Namespace).
			Get(ctx, StateName, metav1.GetOptions{})
		if err != nil {
			return err
		}
		state, err := decodeState(configMap.Data[StateKey])
		if err != nil {
			return err
		}
		if err := mutate(state); err != nil {
			return err
		}
		state.UpdatedAt = s.now()
		data, err := json.Marshal(state)
		if err != nil {
			return err
		}
		configMap.Data[StateKey] = string(data)
		if _, err := s.Client.CoreV1().ConfigMaps(Namespace).
			Update(ctx, configMap, metav1.UpdateOptions{}); err != nil {
			return err
		}
		result = state
		return nil
	})
	if apierrors.IsConflict(err) {
		return nil, apierrors.NewConflict(stateResource, StateName, err)
	}
	return result, err
}

// upsertResource writes one last-writer-wins mirror ConfigMap. Mirrors are
// written by the leader mirror worker. A previous leader may still be exiting,
// so create/update conflicts are re-read and retried.
func (s *Store) upsertResource(ctx context.Context, name, label, key, value string) error {
	configMaps := s.Client.CoreV1().ConfigMaps(Namespace)
	resource := schema.GroupResource{Resource: "configmaps"}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current, err := configMaps.Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			_, err = configMaps.Create(ctx, &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name: name, Labels: map[string]string{label: "true"},
				},
				Data: map[string]string{key: value},
			}, metav1.CreateOptions{})
			if apierrors.IsAlreadyExists(err) {
				return apierrors.NewConflict(resource, name, err)
			}
			return err
		}
		if err != nil {
			return err
		}
		if current.Data[key] == value {
			return nil
		}
		current = current.DeepCopy()
		current.Data[key] = value
		_, err = configMaps.Update(ctx, current, metav1.UpdateOptions{})
		return err
	})
}

func (s *Store) Trust(ctx context.Context) (Trust, error) {
	secret, err := s.Client.CoreV1().Secrets(Namespace).
		Get(ctx, TrustSecretName, metav1.GetOptions{})
	if err != nil {
		return Trust{}, fmt.Errorf("read coordinator trust: %w", err)
	}
	trust := Trust{
		CACert: secret.Data[caCertKey], CAKey: secret.Data[caPrivateKey],
		ServerCert: secret.Data[serverCertKey], ServerKey: secret.Data[serverPrivateKey],
	}
	if len(trust.CACert) == 0 || len(trust.CAKey) == 0 ||
		len(trust.ServerCert) == 0 || len(trust.ServerKey) == 0 {
		return Trust{}, errors.New("coordinator trust secret is incomplete")
	}
	return trust, nil
}

func (s *Store) CAPin(ctx context.Context) (string, error) {
	trust, err := s.Trust(ctx)
	if err != nil {
		return "", err
	}
	cert, err := firstCertificate(trust.CACert)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(cert.Raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func (s *Store) CreateInvitation(ctx context.Context, role string, allowed []string, ttl time.Duration) (Invitation, string, error) {
	if role != layout.RoleAgent && role != layout.RoleServer {
		return Invitation{}, "", fmt.Errorf("invitation role must be agent or server")
	}
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	allowed = append([]string(nil), allowed...)
	for _, capability := range allowed {
		if !slices.Contains(layout.Capabilities, capability) {
			return Invitation{}, "", fmt.Errorf("unknown allowed capability %q", capability)
		}
	}
	slices.Sort(allowed)
	allowed = slices.Compact(allowed)
	id := uuid.NewString()
	pin, err := s.CAPin(ctx)
	if err != nil {
		return Invitation{}, "", err
	}
	encoded, token, err := NewToken(id, pin)
	if err != nil {
		return Invitation{}, "", err
	}
	invitation := Invitation{
		ID: id, Role: role, AllowedCapabilities: allowed,
		ExpiresAt: s.now().Add(ttl),
	}
	annotations := map[string]string{
		annotationRole:    role,
		annotationExpires: invitation.ExpiresAt.Format(time.RFC3339),
	}
	if len(allowed) > 0 {
		annotations[annotationAllowedCaps] = strings.Join(allowed, ",")
	}
	_, err = s.Client.CoreV1().Secrets(Namespace).Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "skali-invitation-" + id,
			Labels:      map[string]string{InvitationLabel: "true"},
			Annotations: annotations,
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{credentialHashKey: []byte(CredentialHash(token.Credential))},
	}, metav1.CreateOptions{})
	if err != nil {
		return Invitation{}, "", fmt.Errorf("create invitation: %w", err)
	}
	return invitation, encoded, nil
}

func (s *Store) Invitations(ctx context.Context) ([]Invitation, error) {
	secrets, err := s.Client.CoreV1().Secrets(Namespace).
		List(ctx, metav1.ListOptions{LabelSelector: InvitationLabel + "=true"})
	if err != nil {
		return nil, err
	}
	result := make([]Invitation, 0, len(secrets.Items))
	for index := range secrets.Items {
		if invitation, err := invitationFromSecret(&secrets.Items[index]); err == nil {
			result = append(result, invitation)
		}
	}
	slices.SortFunc(result, func(a, b Invitation) int {
		return a.ExpiresAt.Compare(b.ExpiresAt)
	})
	return result, nil
}

func (s *Store) RevokeInvitation(ctx context.Context, id string) error {
	secret, err := s.invitationSecret(ctx, id)
	if err != nil {
		return err
	}
	if secret.Annotations == nil {
		secret.Annotations = make(map[string]string)
	}
	secret.Annotations[annotationRevoked] = "true"
	_, err = s.Client.CoreV1().Secrets(Namespace).Update(ctx, secret, metav1.UpdateOptions{})
	return err
}

func (s *Store) CheckInvitation(ctx context.Context, token Token, capabilities []string) (Invitation, error) {
	return s.CheckInvitationFor(ctx, token, "", capabilities)
}

func (s *Store) CheckInvitationFor(ctx context.Context, token Token, installationID string,
	capabilities []string) (Invitation, error) {
	secret, err := s.invitationSecret(ctx, token.Invitation)
	if err != nil {
		return Invitation{}, err
	}
	invitation, err := invitationFromSecret(secret)
	if err != nil {
		return Invitation{}, err
	}
	if invitation.Revoked {
		return Invitation{}, &EnrollmentError{Status: 401, Code: "invitation_revoked", Message: "invitation was revoked"}
	}
	if !s.now().Before(invitation.ExpiresAt) {
		return Invitation{}, &EnrollmentError{Status: 401, Code: "invitation_expired", Message: "invitation expired"}
	}
	if invitation.UsedBy != "" {
		if invitation.UsedBy != installationID {
			return Invitation{}, fmt.Errorf("invitation is already bound to node %s", invitation.UsedBy)
		}
	}
	if !MatchCredential(string(secret.Data[credentialHashKey]), token.Credential) {
		return Invitation{}, &EnrollmentError{Status: 401, Code: "invalid_credential", Message: "invalid invitation credential"}
	}
	if len(capabilities) > 0 {
		if err := validateAllowedCapabilities(invitation, capabilities); err != nil {
			return Invitation{}, err
		}
	}
	return invitation, nil
}

// BindInvitation atomically consumes an invitation. A retry by the same
// installation remains successful; another host is rejected.
func (s *Store) BindInvitation(ctx context.Context, token Token, installationID string,
	capabilities []string, csr []byte) (Invitation, error) {
	var result Invitation
	csrSum := sha256.Sum256(csr)
	csrHash := hex.EncodeToString(csrSum[:])
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		secret, err := s.invitationSecret(ctx, token.Invitation)
		if err != nil {
			return err
		}
		invitation, err := invitationFromSecret(secret)
		if err != nil {
			return err
		}
		if invitation.Revoked {
			return &EnrollmentError{Status: 401, Code: "invitation_revoked", Message: "invitation was revoked"}
		}
		if !s.now().Before(invitation.ExpiresAt) {
			return &EnrollmentError{Status: 401, Code: "invitation_expired", Message: "invitation expired"}
		}
		if !MatchCredential(string(secret.Data[credentialHashKey]), token.Credential) {
			return &EnrollmentError{Status: 401, Code: "invalid_credential", Message: "invalid invitation credential"}
		}
		if err := validateAllowedCapabilities(invitation, capabilities); err != nil {
			return err
		}
		if invitation.UsedBy != "" {
			if invitation.UsedBy == installationID {
				if existing := secret.Annotations[annotationCSRHash]; existing != csrHash {
					return &EnrollmentError{Status: 401, Code: "identity_mismatch", Message: "enrollment retry used a different CSR"}
				}
				result = invitation
				return nil
			}
			return fmt.Errorf("invitation is already bound to another host")
		}
		if secret.Annotations == nil {
			secret.Annotations = make(map[string]string)
		}
		secret.Annotations[annotationUsedBy] = installationID
		secret.Annotations[annotationCSRHash] = csrHash
		if _, err := s.Client.CoreV1().Secrets(Namespace).
			Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
			return err
		}
		invitation.UsedBy = installationID
		result = invitation
		return nil
	})
	return result, err
}

func (s *Store) invitationSecret(ctx context.Context, id string) (*corev1.Secret, error) {
	secret, err := s.Client.CoreV1().Secrets(Namespace).
		Get(ctx, "skali-invitation-"+id, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, errors.New("invitation does not exist")
	}
	if err != nil {
		return nil, fmt.Errorf("read invitation: %w", err)
	}
	return secret, nil
}

func invitationFromSecret(secret *corev1.Secret) (Invitation, error) {
	id := strings.TrimPrefix(secret.Name, "skali-invitation-")
	expires, err := time.Parse(time.RFC3339, secret.Annotations[annotationExpires])
	if err != nil {
		return Invitation{}, errors.New("invitation has invalid expiry")
	}
	allowed := []string(nil)
	if value := secret.Annotations[annotationAllowedCaps]; value != "" {
		allowed = strings.Split(value, ",")
	}
	return Invitation{
		ID: id, Role: secret.Annotations[annotationRole],
		AllowedCapabilities: allowed, ExpiresAt: expires,
		UsedBy:  secret.Annotations[annotationUsedBy],
		Revoked: secret.Annotations[annotationRevoked] == "true",
	}, nil
}

func validateAllowedCapabilities(invitation Invitation, capabilities []string) error {
	if len(capabilities) == 0 {
		return errors.New("at least one capability is required")
	}
	seen := make(map[string]bool, len(capabilities))
	for _, capability := range capabilities {
		if !slices.Contains(layout.Capabilities, capability) {
			return fmt.Errorf("unknown capability %q", capability)
		}
		if seen[capability] {
			return fmt.Errorf("capability %q is repeated", capability)
		}
		seen[capability] = true
		if len(invitation.AllowedCapabilities) > 0 &&
			!slices.Contains(invitation.AllowedCapabilities, capability) {
			return fmt.Errorf("invitation does not allow capability %q", capability)
		}
	}
	return nil
}

func (s *Store) SignCSR(ctx context.Context, csrPEM []byte, nodeID, nodeName string, ttl time.Duration) ([]byte, error) {
	trust, err := s.Trust(ctx)
	if err != nil {
		return nil, err
	}
	ca, caKey, err := parseCA(trust.CACert, trust.CAKey)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(csrPEM)
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return nil, errors.New("agent CSR is malformed")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil || csr.CheckSignature() != nil {
		return nil, errors.New("agent CSR signature is invalid")
	}
	if ttl <= 0 {
		ttl = 30 * 24 * time.Hour
	}
	now := s.now()
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: nodeID, Organization: []string{"skali-nodes"}},
		DNSNames:     []string{nodeName},
		NotBefore:    now.Add(-time.Minute), NotAfter: now.Add(ttl),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, csr.PublicKey, caKey)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nil
}

func generateTrust(cluster string, now time.Time) (Trust, error) {
	_, caKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return Trust{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return Trust{}, err
	}
	caTemplate := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: cluster + " Skali coordinator CA"},
		NotBefore:    now.Add(-time.Minute), NotAfter: now.AddDate(10, 0, 0),
		IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, caKey.Public(), caKey)
	if err != nil {
		return Trust{}, err
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		return Trust{}, err
	}
	_, serverKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return Trust{}, err
	}
	serverSerial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return Trust{}, err
	}
	serverTemplate := &x509.Certificate{
		SerialNumber: serverSerial,
		Subject:      pkix.Name{CommonName: "skali-coordinator"},
		NotBefore:    now.Add(-time.Minute), NotAfter: now.AddDate(2, 0, 0),
		DNSNames:    []string{"skali-coordinator", "localhost"},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	serverDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, caCert, serverKey.Public(), caKey)
	if err != nil {
		return Trust{}, err
	}
	caKeyDER, err := x509.MarshalPKCS8PrivateKey(caKey)
	if err != nil {
		return Trust{}, err
	}
	serverKeyDER, err := x509.MarshalPKCS8PrivateKey(serverKey)
	if err != nil {
		return Trust{}, err
	}
	return Trust{
		CACert: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}),
		CAKey:  pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: caKeyDER}),
		ServerCert: append(
			pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverDER}),
			pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})...),
		ServerKey: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: serverKeyDER}),
	}, nil
}

func caBytes(value []byte) []byte { return append([]byte(nil), value...) }

func firstCertificate(value []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(value)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("coordinator CA contains no certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse coordinator CA: %w", err)
	}
	return cert, nil
}

func parseCA(certPEM, keyPEM []byte) (*x509.Certificate, any, error) {
	cert, err := firstCertificate(certPEM)
	if err != nil {
		return nil, nil, err
	}
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, nil, errors.New("coordinator CA key is malformed")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("parse coordinator CA key: %w", err)
	}
	return cert, key, nil
}
