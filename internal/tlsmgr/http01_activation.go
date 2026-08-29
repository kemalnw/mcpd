package tlsmgr

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-acme/lego/v5/challenge/http01"
)

type activatedHTTP01Provider struct {
	factory func() (net.Listener, error)

	mu        sync.RWMutex
	server    *http.Server
	listener  net.Listener
	done      chan struct{}
	challenge *http01Challenge
}

type http01Challenge struct {
	domain  string
	path    string
	keyAuth string
}

func newActivatedHTTP01Provider(factory func() (net.Listener, error)) *activatedHTTP01Provider {
	return &activatedHTTP01Provider{factory: factory}
}
func (p *activatedHTTP01Provider) Present(_ context.Context, domain, token, keyAuth string) error {
	if err := p.ensureServer(); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.challenge != nil {
		return errors.New("ACME HTTP-01 challenge is already active")
	}
	p.challenge = &http01Challenge{domain: domain, path: http01.ChallengePath(token), keyAuth: keyAuth}
	return nil
}

func (p *activatedHTTP01Provider) CleanUp(_ context.Context, domain, token, _ string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.challenge == nil {
		return nil
	}
	if p.challenge.domain == domain && p.challenge.path == http01.ChallengePath(token) {
		p.challenge = nil
	}
	return nil
}

func (p *activatedHTTP01Provider) ensureServer() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.server != nil {
		return nil
	}
	listener, err := p.factory()
	if err != nil {
		return fmt.Errorf("open activated ACME listener: %w", err)
	}
	server := &http.Server{
		Handler: http.HandlerFunc(p.serveHTTP), ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second,
	}
	server.SetKeepAlivesEnabled(false)
	p.server, p.listener, p.done = server, listener, make(chan struct{})
	go func(done chan struct{}) {
		defer close(done)
		_ = server.Serve(listener)
	}(p.done)
	return nil
}

func (p *activatedHTTP01Provider) serveHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.RLock()
	challenge := p.challenge
	p.mu.RUnlock()
	if challenge == nil || r.Method != http.MethodGet || r.URL.Path != challenge.path || !sameHost(r.Host, challenge.domain) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write([]byte(challenge.keyAuth))
}
func (p *activatedHTTP01Provider) Close(ctx context.Context) error {
	p.mu.Lock()
	server, listener, done := p.server, p.listener, p.done
	p.server, p.listener, p.done, p.challenge = nil, nil, nil, nil
	p.mu.Unlock()
	if server == nil {
		return nil
	}
	shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	err := server.Shutdown(shutdownCtx)
	_ = listener.Close()
	select {
	case <-done:
	case <-shutdownCtx.Done():
		if err == nil {
			err = shutdownCtx.Err()
		}
	}
	return err
}

func sameHost(rawHost, domain string) bool {
	host := rawHost
	if parsed, _, err := net.SplitHostPort(rawHost); err == nil {
		host = parsed
	}
	host = strings.Trim(host, "[]")
	domain = strings.Trim(domain, "[]")
	if left, right := net.ParseIP(host), net.ParseIP(domain); left != nil && right != nil {
		return left.Equal(right)
	}
	return strings.EqualFold(host, domain)
}
