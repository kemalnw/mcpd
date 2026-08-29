package tlsmgr

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-acme/lego/v5/acme"
	"github.com/go-acme/lego/v5/certcrypto"
	"github.com/go-acme/lego/v5/certificate"
	"github.com/go-acme/lego/v5/challenge/http01"
	"github.com/go-acme/lego/v5/lego"
	"github.com/go-acme/lego/v5/registration"
)

type Options struct {
	Mode            string
	CertFile        string
	KeyFile         string
	ExternalURL     string
	ACMEEmail       string
	ACMEServer      string
	ACMEProfile     string
	ACMEAcceptTOS   bool
	ChallengeListen string
	CertDir         string
	RenewCheck      time.Duration
	Logger          *slog.Logger
}

type Manager struct {
	opts    Options
	logger  *slog.Logger
	current atomic.Pointer[tls.Certificate]
	mu      sync.Mutex
}

func New(opts Options) (*Manager, error) {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Mode == "off" {
		return nil, nil
	}
	if opts.RenewCheck <= 0 {
		opts.RenewCheck = time.Hour
	}
	m := &Manager{opts: opts, logger: opts.Logger}
	return m, nil
}

func (m *Manager) Prepare(ctx context.Context) error {
	if m == nil {
		return nil
	}
	switch m.opts.Mode {
	case "files":
		return m.loadPair(m.opts.CertFile, m.opts.KeyFile)
	case "acme":
		if err := os.MkdirAll(m.opts.CertDir, 0o700); err != nil {
			return fmt.Errorf("create TLS state directory: %w", err)
		}
		if err := os.Chmod(m.opts.CertDir, 0o700); err != nil {
			return fmt.Errorf("chmod TLS state directory: %w", err)
		}
		certPath, keyPath := m.certificatePaths()
		if err := m.loadPair(certPath, keyPath); err == nil {
			if !m.needsRenewal(time.Now()) {
				return nil
			}
			m.logger.Info("ACME certificate entered renewal window")
		} else if !errors.Is(err, os.ErrNotExist) {
			m.logger.Warn("existing ACME certificate could not be loaded; obtaining a replacement", "error", err)
		}
		return m.obtain(ctx, certPath, keyPath, true)
	default:
		return fmt.Errorf("unsupported TLS mode %q", m.opts.Mode)
	}
}

func (m *Manager) TLSConfig() *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			cert := m.current.Load()
			if cert == nil {
				return nil, errors.New("TLS certificate is not loaded")
			}
			return cert, nil
		},
		NextProtos: []string{"h2", "http/1.1"},
	}
}

func (m *Manager) RunRenewal(ctx context.Context) {
	if m == nil || m.opts.Mode != "acme" {
		return
	}
	ticker := time.NewTicker(m.opts.RenewCheck)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !m.needsRenewal(time.Now()) {
				continue
			}
			certPath, keyPath := m.certificatePaths()
			if err := m.obtain(ctx, certPath, keyPath, true); err != nil && ctx.Err() == nil {
				m.logger.Error("ACME certificate renewal failed", "error", err)
			}
		}
	}
}

func (m *Manager) loadPair(certPath, keyPath string) error {
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return err
	}
	if len(pair.Certificate) == 0 {
		return errors.New("TLS certificate chain is empty")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return fmt.Errorf("parse TLS leaf certificate: %w", err)
	}
	pair.Leaf = leaf
	m.current.Store(&pair)
	return nil
}

func (m *Manager) needsRenewal(now time.Time) bool {
	cert := m.current.Load()
	if cert == nil || cert.Leaf == nil {
		return true
	}
	leaf := cert.Leaf
	if !now.Before(leaf.NotAfter) {
		return true
	}
	lifetime := leaf.NotAfter.Sub(leaf.NotBefore)
	if lifetime <= 0 {
		return true
	}
	// Renew halfway through the certificate lifetime. This is appropriate for
	// short-lived public IP certificates and provides ample retry budget.
	return !now.Before(leaf.NotBefore.Add(lifetime / 2))
}

func (m *Manager) obtain(ctx context.Context, certPath, keyPath string, renewal bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if renewal && !m.needsRenewal(time.Now()) {
		return nil
	}

	client, err := m.newACMEClient(ctx)
	if err != nil {
		return err
	}
	host, err := certificateHost(m.opts.ExternalURL)
	if err != nil {
		return err
	}
	h, p, err := net.SplitHostPort(m.opts.ChallengeListen)
	if err != nil {
		return fmt.Errorf("parse ACME challenge listen address: %w", err)
	}
	if err := client.Challenge.SetHTTP01Provider(http01.NewProviderServer(h, p)); err != nil {
		return fmt.Errorf("configure ACME HTTP-01 challenge: %w", err)
	}

	request := certificate.ObtainRequest{Domains: []string{host}, KeyType: certcrypto.EC256, Bundle: true, Profile: m.opts.ACMEProfile}
	if existingCert, existingKey, readErr := readExistingPair(certPath, keyPath); readErr == nil && renewal {
		resource := certificate.Resource{Domains: []string{host}, Certificate: existingCert, PrivateKey: existingKey, Profile: m.opts.ACMEProfile}
		res, renewErr := client.Certificate.Renew(ctx, resource, &certificate.RenewOptions{Bundle: true, Profile: m.opts.ACMEProfile})
		if renewErr == nil {
			return m.persistAndLoad(res, certPath, keyPath)
		}
		m.logger.Warn("ACME renew failed; requesting a fresh certificate", "error", renewErr)
	}
	res, err := client.Certificate.Obtain(ctx, request)
	if err != nil {
		return fmt.Errorf("obtain ACME certificate for %s: %w", host, err)
	}
	return m.persistAndLoad(res, certPath, keyPath)
}

func (m *Manager) newACMEClient(ctx context.Context) (*lego.Client, error) {
	key, err := m.loadOrCreateAccountKey()
	if err != nil {
		return nil, err
	}
	user := &acmeUser{email: m.opts.ACMEEmail, key: key}
	cfg := lego.NewConfig(user)
	cfg.CADirURL = m.opts.ACMEServer
	client, err := lego.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("create ACME client: %w", err)
	}
	reg, err := client.Registration.Register(ctx, registration.RegisterOptions{TermsOfServiceAgreed: m.opts.ACMEAcceptTOS})
	if err != nil {
		return nil, fmt.Errorf("register/resolve ACME account: %w", err)
	}
	user.registration = reg
	return client, nil
}

func (m *Manager) loadOrCreateAccountKey() (*ecdsa.PrivateKey, error) {
	path := filepath.Join(m.opts.CertDir, "acme-account.key")
	data, err := os.ReadFile(path)
	if err == nil {
		block, _ := pem.Decode(data)
		if block == nil || block.Type != "EC PRIVATE KEY" {
			return nil, errors.New("invalid ACME account key PEM")
		}
		key, parseErr := x509.ParseECPrivateKey(block.Bytes)
		if parseErr != nil {
			return nil, fmt.Errorf("parse ACME account key: %w", parseErr)
		}
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read ACME account key: %w", err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate ACME account key: %w", err)
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	if err := atomicWrite(path, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		return nil, fmt.Errorf("persist ACME account key: %w", err)
	}
	return key, nil
}

func (m *Manager) persistAndLoad(resource *certificate.Resource, certPath, keyPath string) error {
	if resource == nil || len(resource.Certificate) == 0 || len(resource.PrivateKey) == 0 {
		return errors.New("ACME server returned incomplete certificate resource")
	}
	if err := atomicWrite(certPath, resource.Certificate, 0o644); err != nil {
		return fmt.Errorf("persist TLS certificate: %w", err)
	}
	if err := atomicWrite(keyPath, resource.PrivateKey, 0o600); err != nil {
		return fmt.Errorf("persist TLS private key: %w", err)
	}
	if err := m.loadPair(certPath, keyPath); err != nil {
		return fmt.Errorf("load renewed TLS certificate: %w", err)
	}
	leaf := m.current.Load().Leaf
	m.logger.Info("TLS certificate loaded", "not_before", leaf.NotBefore, "not_after", leaf.NotAfter)
	return nil
}

func (m *Manager) certificatePaths() (string, string) {
	return filepath.Join(m.opts.CertDir, "server.crt"), filepath.Join(m.opts.CertDir, "server.key")
}

func certificateHost(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return "", errors.New("external URL must be an HTTPS origin")
	}
	return u.Hostname(), nil
}

func readExistingPair(certPath, keyPath string) ([]byte, []byte, error) {
	cert, err := os.ReadFile(certPath)
	if err != nil {
		return nil, nil, err
	}
	key, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, nil, err
	}
	return cert, key, nil
}

type acmeUser struct {
	email        string
	registration *acme.ExtendedAccount
	key          *ecdsa.PrivateKey
}

func (u *acmeUser) GetEmail() string                       { return u.email }
func (u *acmeUser) GetRegistration() *acme.ExtendedAccount { return u.registration }
func (u *acmeUser) GetPrivateKey() crypto.Signer           { return u.key }

var _ registration.User = (*acmeUser)(nil)

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".mcpd-tls-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
