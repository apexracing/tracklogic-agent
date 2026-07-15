package builtin

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/apexracing/tracklogic-agent/model"
	"github.com/apexracing/tracklogic-agent/tool"
)

const (
	defaultHTTPTimeout = 10 * time.Second
	maximumHTTPTimeout = 30 * time.Second
	maxHTTPBodyBytes   = 64 * 1024
	maxHTTPRedirects   = 5
)

var carrierNATNetwork = mustParseCIDR("100.64.0.0/10")

type HTTPGetConfig struct {
	// AllowedHosts is an optional exact-host or *.suffix allowlist. An empty
	// list permits any host that passes the network-address policy.
	AllowedHosts         []string
	AllowPrivateNetworks bool
	MaxTimeout           time.Duration
	MaxBodyBytes         int64
	HTTPClient           *http.Client
}

type HTTPGetTool struct {
	tool.BaseTool
	client               *http.Client
	allowedHosts         []string
	allowPrivateNetworks bool
	maxTimeout           time.Duration
	maxBodyBytes         int64
	resolver             *net.Resolver
}

func NewHTTPGet() *HTTPGetTool {
	return NewHTTPGetWithConfig(HTTPGetConfig{})
}

func NewHTTPGetWithConfig(config HTTPGetConfig) *HTTPGetTool {
	if config.MaxTimeout <= 0 {
		config.MaxTimeout = maximumHTTPTimeout
	}
	if config.MaxBodyBytes <= 0 {
		config.MaxBodyBytes = maxHTTPBodyBytes
	}
	allowedHosts := make([]string, 0, len(config.AllowedHosts))
	for _, host := range config.AllowedHosts {
		if normalized := strings.ToLower(strings.TrimSpace(host)); normalized != "" {
			allowedHosts = append(allowedHosts, normalized)
		}
	}

	runtimeTool := &HTTPGetTool{
		BaseTool: tool.NewBaseTool(
			"http_get",
			"Send a bounded HTTP(S) GET request. Private networks are denied by default.",
			[]model.ToolParameter{
				{Name: "url", Type: "string", Description: "HTTP(S) URL to fetch", Required: true},
				{Name: "timeout_seconds", Type: "number", Description: "Request timeout in seconds (default 10, bounded by policy)", Required: false},
			},
		),
		allowedHosts:         allowedHosts,
		allowPrivateNetworks: config.AllowPrivateNetworks,
		maxTimeout:           config.MaxTimeout,
		maxBodyBytes:         config.MaxBodyBytes,
		resolver:             net.DefaultResolver,
	}
	runtimeTool.client = runtimeTool.buildClient(config.HTTPClient)
	return runtimeTool
}

func (t *HTTPGetTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	urlString, _ := args["url"].(string)
	if urlString == "" {
		return nil, fmt.Errorf("url is required")
	}
	parsed, err := url.Parse(urlString)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}
	if err := t.validateURL(ctx, parsed); err != nil {
		return nil, err
	}

	timeout := defaultHTTPTimeout
	switch value := args["timeout_seconds"].(type) {
	case float64:
		if value > 0 {
			timeout = time.Duration(value * float64(time.Second))
		}
	case int:
		if value > 0 {
			timeout = time.Duration(value) * time.Second
		}
	}
	if timeout > t.maxTimeout {
		timeout = t.maxTimeout
	}

	requestContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("invalid request: %w", err)
	}
	request.Header.Set("User-Agent", "tracklogic-agent/http_get")

	response, err := t.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("http get failed: %w", err)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(io.LimitReader(response.Body, t.maxBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("failed to read body: %w", err)
	}
	truncated := int64(len(body)) > t.maxBodyBytes
	if truncated {
		body = body[:t.maxBodyBytes]
	}
	return map[string]any{
		"status": response.StatusCode, "content_type": response.Header.Get("Content-Type"),
		"body": string(body), "truncated": truncated, "content_length": len(body),
	}, nil
}

func (t *HTTPGetTool) buildClient(configured *http.Client) *http.Client {
	var client http.Client
	if configured != nil {
		client = *configured
	} else {
		client.Transport = &http.Transport{
			Proxy:       nil,
			DialContext: t.secureDialContext,
		}
	}
	previousRedirectCheck := client.CheckRedirect
	client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) >= maxHTTPRedirects {
			return fmt.Errorf("stopped after %d redirects", maxHTTPRedirects)
		}
		if err := t.validateURL(request.Context(), request.URL); err != nil {
			return fmt.Errorf("redirect rejected: %w", err)
		}
		if previousRedirectCheck != nil {
			return previousRedirectCheck(request, via)
		}
		return nil
	}
	return &client
}

func (t *HTTPGetTool) validateURL(ctx context.Context, parsed *url.URL) error {
	if parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("only http and https URLs are allowed")
	}
	if parsed.User != nil {
		return fmt.Errorf("URLs containing user information are not allowed")
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		return fmt.Errorf("URL host is required")
	}
	if !t.hostAllowed(host) {
		return fmt.Errorf("host %q is not in the allowlist", host)
	}
	if t.allowPrivateNetworks {
		return nil
	}
	addresses, err := t.resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return fmt.Errorf("resolve host %q: %w", host, err)
	}
	if len(addresses) == 0 {
		return fmt.Errorf("host %q resolved to no addresses", host)
	}
	for _, address := range addresses {
		if !isPublicIP(address.IP) {
			return fmt.Errorf("host %q resolves to disallowed address %s", host, address.IP)
		}
	}
	return nil
}

func (t *HTTPGetTool) hostAllowed(host string) bool {
	if len(t.allowedHosts) == 0 {
		return true
	}
	for _, allowed := range t.allowedHosts {
		if host == allowed {
			return true
		}
		if strings.HasPrefix(allowed, "*.") {
			suffix := strings.TrimPrefix(allowed, "*")
			if strings.HasSuffix(host, suffix) && host != strings.TrimPrefix(suffix, ".") {
				return true
			}
		}
	}
	return false
}

func (t *HTTPGetTool) secureDialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	addresses, err := t.resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	dialer := net.Dialer{}
	var lastError error
	for _, resolved := range addresses {
		if !t.allowPrivateNetworks && !isPublicIP(resolved.IP) {
			continue
		}
		connection, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(resolved.IP.String(), port))
		if dialErr == nil {
			return connection, nil
		}
		lastError = dialErr
	}
	if lastError != nil {
		return nil, lastError
	}
	return nil, fmt.Errorf("host %q has no permitted addresses", host)
}

func isPublicIP(ip net.IP) bool {
	if ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
		return false
	}
	// Carrier-grade NAT is not reported by net.IP.IsPrivate but is not a
	// suitable public destination for a general-purpose fetch tool.
	return !carrierNATNetwork.Contains(ip)
}

func mustParseCIDR(value string) *net.IPNet {
	_, network, err := net.ParseCIDR(value)
	if err != nil {
		panic(err)
	}
	return network
}
