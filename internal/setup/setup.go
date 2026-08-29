package setup

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/kemalnw/mcpd/internal/config"
)

type HTTPSMode string

const (
	HTTPSModeCaddy    HTTPSMode = "caddy"
	HTTPSModeExternal HTTPSMode = "external"
)

type Input struct {
	PublicOrigin string
	Listen       string
	MCPPath      string
	ServiceUser  string
	OAuthEnabled bool
	HTTPSMode    HTTPSMode
}

type Plan struct {
	Config       config.Config
	PublicOrigin string
	ServiceUser  string
	HTTPSMode    HTTPSMode
}

func Build(input Input) (Plan, error) {
	return BuildFrom(config.Default(), input)
}

func BuildFrom(base config.Config, input Input) (Plan, error) {
	origin, err := canonicalOrigin(input.PublicOrigin)
	if err != nil {
		return Plan{}, err
	}
	cfg := base
	if input.Listen != "" {
		cfg.Server.Listen = input.Listen
	}
	if input.MCPPath != "" {
		cfg.Server.MCPPath = input.MCPPath
	}
	cfg.Auth.Enabled = input.OAuthEnabled
	if input.OAuthEnabled {
		cfg.Auth.ExternalURL = origin
	}
	if err := cfg.Validate(); err != nil {
		return Plan{}, err
	}
	if strings.TrimSpace(input.ServiceUser) == "" {
		return Plan{}, errors.New("service user must not be empty")
	}
	httpsMode := input.HTTPSMode
	if httpsMode == "" {
		httpsMode = HTTPSModeCaddy
	}
	if httpsMode != HTTPSModeCaddy && httpsMode != HTTPSModeExternal {
		return Plan{}, fmt.Errorf("unsupported HTTPS mode %q", httpsMode)
	}
	return Plan{Config: cfg, PublicOrigin: origin, ServiceUser: input.ServiceUser, HTTPSMode: httpsMode}, nil
}

func canonicalOrigin(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("public domain or HTTPS origin is required")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	if err := config.ValidateExternalURL(raw); err != nil {
		return "", err
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	return u.Scheme + "://" + u.Host, nil
}

func (p Plan) ManagedCaddy() bool { return p.HTTPSMode == HTTPSModeCaddy }

func (p Plan) PublicHost() (string, error) {
	u, err := url.Parse(p.PublicOrigin)
	if err != nil {
		return "", err
	}
	if u.Hostname() == "" {
		return "", errors.New("public origin has no hostname")
	}
	return u.Hostname(), nil
}

func (p Plan) CaddyBackend() (string, error) {
	host, port, err := net.SplitHostPort(p.Config.Server.Listen)
	if err != nil {
		return "", fmt.Errorf("parse backend listen address: %w", err)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port), nil
}

func (p Plan) PublicMCPURL() string {
	return strings.TrimSuffix(p.PublicOrigin, "/") + p.Config.Server.MCPPath
}

func (p Plan) BackendURL() string {
	return "http://" + p.Config.Server.Listen
}

func (p Plan) HealthURL() (string, error) {
	host, port, err := net.SplitHostPort(p.Config.Server.Listen)
	if err != nil {
		return "", fmt.Errorf("parse backend listen address: %w", err)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/healthz", nil
}

type Executor interface {
	Preflight(Plan) error
	Install(Plan) error
	ConfigurePassword([]byte) error
	Restart() error
	HealthCheck(context.Context, string) error
	ConfigureFrontend(Plan) error
	PublicHealthCheck(context.Context, Plan) error
	Doctor() error
}

type ApplyOptions struct {
	Password []byte
}

func Apply(ctx context.Context, plan Plan, opts ApplyOptions, exec Executor) error {
	if err := exec.Preflight(plan); err != nil {
		return fmt.Errorf("preflight: %w", err)
	}
	if err := exec.Install(plan); err != nil {
		return fmt.Errorf("install service: %w", err)
	}
	if plan.Config.Auth.Enabled && len(opts.Password) > 0 {
		if err := exec.ConfigurePassword(opts.Password); err != nil {
			return fmt.Errorf("configure owner password: %w", err)
		}
	}
	if err := exec.Restart(); err != nil {
		return fmt.Errorf("start service: %w", err)
	}
	healthURL, err := plan.HealthURL()
	if err != nil {
		return err
	}
	if err := exec.HealthCheck(ctx, healthURL); err != nil {
		return fmt.Errorf("local health check: %w", err)
	}
	if err := exec.ConfigureFrontend(plan); err != nil {
		return fmt.Errorf("configure HTTPS frontend: %w", err)
	}
	if err := exec.PublicHealthCheck(ctx, plan); err != nil {
		return fmt.Errorf("public HTTPS check: %w", err)
	}
	if err := exec.Doctor(); err != nil {
		return fmt.Errorf("doctor: %w", err)
	}
	return nil
}
