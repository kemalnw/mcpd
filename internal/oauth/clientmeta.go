package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxClientMetadataBytes = 1 << 20

type clientMetadata struct {
	ClientID                          string   `json:"client_id"`
	ClientName                        string   `json:"client_name"`
	RedirectURIs                      []string `json:"redirect_uris"`
	GrantTypes                        []string `json:"grant_types"`
	ResponseTypes                     []string `json:"response_types"`
	TokenEndpointAuthMethodsSupported []string `json:"token_endpoint_auth_methods_supported"`
	TokenEndpointAuthMethod           string   `json:"token_endpoint_auth_method"`
}

func newSafeHTTPClient(timeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	dialer := &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		for _, resolved := range ips {
			if !publicIP(resolved.IP) {
				continue
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(resolved.IP.String(), port))
		}
		return nil, errors.New("client metadata host did not resolve to a public IP")
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("client metadata redirects are not allowed")
		},
	}
}

func publicIP(ip net.IP) bool {
	return ip != nil && !ip.IsLoopback() && !ip.IsPrivate() && !ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast() &&
		!ip.IsUnspecified() && !ip.IsMulticast()
}

func validateClientID(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" || u.Path == "" || u.Path == "/" || hasDotSegment(u.EscapedPath()) {
		return nil, errors.New("client_id must be an absolute HTTPS Client ID Metadata Document URL with a path")
	}
	return u, nil
}

func hasDotSegment(path string) bool {
	for _, segment := range strings.Split(path, "/") {
		decoded, err := url.PathUnescape(segment)
		if err != nil || decoded == "." || decoded == ".." {
			return true
		}
	}
	return false
}

func fetchClientMetadata(ctx context.Context, client *http.Client, clientID string) (clientMetadata, error) {
	if _, err := validateClientID(clientID); err != nil {
		return clientMetadata{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, clientID, nil)
	if err != nil {
		return clientMetadata{}, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return clientMetadata{}, fmt.Errorf("fetch client metadata: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return clientMetadata{}, fmt.Errorf("client metadata returned HTTP %d", resp.StatusCode)
	}
	limited := io.LimitReader(resp.Body, maxClientMetadataBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return clientMetadata{}, fmt.Errorf("read client metadata: %w", err)
	}
	if len(data) > maxClientMetadataBytes {
		return clientMetadata{}, errors.New("client metadata document exceeds 1 MiB")
	}
	var metadata clientMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return clientMetadata{}, fmt.Errorf("decode client metadata: %w", err)
	}
	if metadata.ClientID != clientID {
		return clientMetadata{}, errors.New("client metadata client_id does not exactly match its document URL")
	}
	if strings.TrimSpace(metadata.ClientName) == "" {
		return clientMetadata{}, errors.New("client metadata does not declare client_name")
	}
	if len(metadata.RedirectURIs) == 0 {
		return clientMetadata{}, errors.New("client metadata does not declare redirect_uris")
	}
	methods := metadata.TokenEndpointAuthMethodsSupported
	if len(methods) == 0 && metadata.TokenEndpointAuthMethod != "" {
		methods = []string{metadata.TokenEndpointAuthMethod}
	}
	if !contains(methods, "none") {
		return clientMetadata{}, errors.New("client metadata does not support token endpoint auth method none")
	}
	if len(metadata.ResponseTypes) > 0 && !contains(metadata.ResponseTypes, "code") {
		return clientMetadata{}, errors.New("client metadata does not support response_type=code")
	}
	if len(metadata.GrantTypes) > 0 && !contains(metadata.GrantTypes, "authorization_code") {
		return clientMetadata{}, errors.New("client metadata does not support authorization_code grant")
	}
	return metadata, nil
}

func validateRedirectURI(metadata clientMetadata, redirect string) error {
	if !contains(metadata.RedirectURIs, redirect) {
		return errors.New("redirect_uri is not registered by the client metadata document")
	}
	u, err := url.Parse(redirect)
	if err != nil || u.Host == "" || u.Fragment != "" {
		return errors.New("redirect_uri must be an absolute URI without a fragment")
	}
	if u.Scheme == "https" {
		return nil
	}
	host := strings.Trim(u.Hostname(), "[]")
	if u.Scheme == "http" && (host == "localhost" || net.ParseIP(host).IsLoopback()) {
		return nil
	}
	return errors.New("redirect_uri must use HTTPS except for loopback clients")
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
