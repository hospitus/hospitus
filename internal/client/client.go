// Package client provides an HTTP client for the HOSPITUS API
package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/hospitus/hospitus/pkg/provider"
)

// DefaultTimeout is the default HTTP client timeout
const DefaultTimeout = 30 * time.Second

// StreamingTimeout is the timeout for streaming operations (exec, create, image fetch)
// These operations can take a long time but should still have a reasonable limit
const StreamingTimeout = 30 * time.Minute

// Client is an HTTP client for the HOSPITUS API
type Client struct {
	baseURL    string
	httpClient *http.Client
	timeout    time.Duration
	apiKey     string
	// tlsErr holds a TLS configuration error captured at construction time
	// (NewClientWithOptions cannot return an error without breaking callers).
	// It is surfaced on the first request via configError.
	tlsErr error
}

// configError returns any deferred client configuration error (such as an
// invalid TLS/CA setting captured at construction) so it can be surfaced on
// first use instead of being silently ignored.
func (c *Client) configError() error {
	if c.tlsErr != nil {
		return fmt.Errorf("TLS configuration error: %w", c.tlsErr)
	}
	return nil
}

// streamingTransport returns an http.Transport configured for streaming operations
// with reasonable connection and header timeouts to prevent slowloris-style attacks.
// It is used by streaming operations (exec, image download, instance creation).
func streamingTransport() *http.Transport {
	return &http.Transport{
		ResponseHeaderTimeout: 30 * time.Second,
		IdleConnTimeout:       60 * time.Second,
		ExpectContinueTimeout: 10 * time.Second,
	}
}

// newStreamingClient returns an *http.Client for long-lived streaming requests.
// It keeps the streaming-specific timeouts but inherits the configured TLS
// (custom CA / skip-verify) and redirect handling (which strips the API key on
// cross-origin redirects) from the main client. A bare streamingTransport()
// would otherwise ignore the client's TLS configuration entirely.
func (c *Client) newStreamingClient() *http.Client {
	st := streamingTransport()
	if base, ok := c.httpClient.Transport.(*http.Transport); ok {
		st.TLSClientConfig = base.TLSClientConfig
	}
	return &http.Client{
		Timeout:       StreamingTimeout,
		Transport:     st,
		CheckRedirect: c.httpClient.CheckRedirect,
	}
}

// setAuthHeader sets the X-API-Key header when an API key is configured.
func (c *Client) setAuthHeader(req *http.Request) {
	if c.apiKey != "" {
		req.Header.Set("X-API-Key", c.apiKey)
	}
}

// getTimeoutFromEnv reads the HOSPITUS_TIMEOUT environment variable
// and returns the timeout duration. Accepts formats like "30s", "5m", or just seconds as integer.
// Returns DefaultTimeout if not set or invalid.
func getTimeoutFromEnv() time.Duration {
	timeoutStr := os.Getenv("HOSPITUS_TIMEOUT")
	if timeoutStr == "" {
		return DefaultTimeout
	}

	// Try parsing as duration string (e.g., "30s", "5m", "2m30s")
	if d, err := time.ParseDuration(timeoutStr); err == nil {
		return d
	}

	// Try parsing as integer seconds (e.g., "30" means 30 seconds)
	if secs, err := strconv.Atoi(timeoutStr); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}

	// Invalid format, use default
	return DefaultTimeout
}

// ClientOptions configures optional client behavior.
type ClientOptions struct {
	// APIKey is sent as the X-API-Key header on every request.
	APIKey string

	// TLSSkipVerify disables TLS certificate verification.
	// Only use on private networks or for testing — never in production.
	TLSSkipVerify bool

	// TLSCACert is the path to a PEM-encoded CA certificate bundle used to
	// verify the server certificate. Useful with self-signed certificates.
	TLSCACert string
}

// NewClient creates a new HOSPITUS API client
// The timeout can be configured via the HOSPITUS_TIMEOUT environment variable.
// Accepts formats like "30s", "5m", "2m30s", or just seconds as integer (e.g., "60").
// Defaults to 30 seconds if not set.
func NewClient(baseURL string) *Client {
	return NewClientWithOptions(baseURL, ClientOptions{})
}

// NewClientWithOptions creates a new HOSPITUS API client with optional auth/TLS settings.
func NewClientWithOptions(baseURL string, opts ClientOptions) *Client {
	if !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
		// Default to https when TLS options are configured, http otherwise
		if opts.TLSSkipVerify || opts.TLSCACert != "" {
			baseURL = "https://" + baseURL
		} else {
			baseURL = "http://" + baseURL
		}
	}

	timeout := getTimeoutFromEnv()

	transport := &http.Transport{}
	var tlsErr error
	if opts.TLSSkipVerify || opts.TLSCACert != "" {
		tlsCfg, err := buildTLSConfig(opts)
		if err != nil {
			tlsErr = err
		} else {
			transport.TLSClientConfig = tlsCfg
		}
	}

	return &Client{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		httpClient: &http.Client{
			Timeout:   timeout,
			Transport: transport,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				// Limit redirects to prevent infinite loops
				if len(via) >= 10 {
					return fmt.Errorf("too many redirects")
				}
				// Strip API key on cross-origin redirects to prevent credential
				// leakage. The scheme counts as much as the host: a redirect
				// from https to http on the same name kept the key and sent it
				// in clear text.
				if req.URL.Host != via[0].URL.Host || req.URL.Scheme != via[0].URL.Scheme {
					req.Header.Del("X-API-Key")
				}
				return nil
			},
		},
		timeout: timeout,
		apiKey:  opts.APIKey,
		tlsErr:  tlsErr,
	}
}

// buildTLSConfig constructs a *tls.Config from ClientOptions.
func buildTLSConfig(opts ClientOptions) (*tls.Config, error) {
	cfg := &tls.Config{
		InsecureSkipVerify: opts.TLSSkipVerify, //nolint:gosec // user-explicit opt-in
	}
	if opts.TLSCACert != "" {
		pem, err := os.ReadFile(opts.TLSCACert)
		if err != nil {
			return nil, fmt.Errorf("failed to read CA cert %q: %w", opts.TLSCACert, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("no valid certificates found in %q", opts.TLSCACert)
		}
		cfg.RootCAs = pool
	}
	return cfg, nil
}

// Health checks the API server health
func (c *Client) Health(ctx context.Context) (map[string]interface{}, error) {
	var response map[string]interface{}
	if err := c.doRequest(ctx, "GET", "/health", nil, &response); err != nil {
		return nil, err
	}
	return response, nil
}

// ListProviders returns all available providers
func (c *Client) ListProviders(ctx context.Context) ([]provider.ProviderInfo, error) {
	var providers []provider.ProviderInfo
	if err := c.doRequest(ctx, "GET", "/api/v1/providers", nil, &providers); err != nil {
		return nil, err
	}
	return providers, nil
}

// GetProvider returns details about a specific provider
func (c *Client) GetProvider(ctx context.Context, name string) (*ProviderDetail, error) {
	var detail ProviderDetail
	path := fmt.Sprintf("/api/v1/providers/%s", url.PathEscape(name))
	if err := c.doRequest(ctx, "GET", path, nil, &detail); err != nil {
		return nil, err
	}
	return &detail, nil
}

// ProviderDetail contains provider metadata and capabilities
type ProviderDetail struct {
	Metadata     provider.ProviderMetadata     `json:"metadata"`
	Capabilities provider.ProviderCapabilities `json:"capabilities"`
}

// doRequest performs an HTTP request with the client's configured timeout
func (c *Client) doRequest(ctx context.Context, method, path string, body, result interface{}) error {
	return c.doRequestWithTimeout(ctx, method, path, body, result, c.timeout)
}

// APIError turns a failed HTTP response into the error a user reads.
//
// Every call path has to route through it, streaming ones included. A path that
// prints the raw response body instead shows the reader a line of JSON rather
// than the message inside it.
func APIError(statusCode int, body []byte) error {
	// "detail" carries the reason a provider rejected the operation; without it
	// the caller is left with a generic sentence and has to read the daemon log.
	var errResp struct {
		Error  string `json:"error"`
		Detail string `json:"detail"`
	}

	msg := ""
	if json.Unmarshal(body, &errResp) == nil && errResp.Error != "" {
		msg = errResp.Error
		if errResp.Detail != "" {
			msg += ": " + errResp.Detail
		}
	} else {
		msg = strings.TrimSpace(string(body))
		if msg == "" {
			msg = http.StatusText(statusCode)
		}
	}

	switch statusCode {
	case http.StatusUnauthorized:
		return fmt.Errorf("authentication failed: %s\n  → Check your API key with 'hospitus context show' or set HOSPITUS_API_KEY", msg)
	case http.StatusForbidden:
		return fmt.Errorf("permission denied: %s", msg)
	case http.StatusNotFound:
		return fmt.Errorf("not found: %s", msg)
	case http.StatusConflict:
		return fmt.Errorf("conflict: %s", msg)
	case http.StatusBadRequest:
		// Go's HTTP server answers a plaintext request on a TLS listener with
		// this, and passing it through explains nothing: the URL is wrong, not
		// the request. It catches people who run one command as themselves and
		// the next under doas, since the context is per user and root's falls
		// back to http://.
		if strings.Contains(msg, "HTTP request to an HTTPS server") {
			return fmt.Errorf("the daemon is using TLS but this URL is http://\n" +
				"  → check which URL is in use with 'hospitus context show'\n" +
				"  → running under doas uses root's context, not yours; give root one too,\n" +
				"    or pass --api-url https://... and --tls-ca")
		}
		return fmt.Errorf("invalid request: %s", msg)
	case http.StatusTooManyRequests:
		return fmt.Errorf("rate limited by the daemon: %s\n  → the daemon accepts a limited number of requests per second per client\n  → space the commands out, or raise --rate-limit-rps on hospitusd", msg)
	case http.StatusServiceUnavailable:
		return fmt.Errorf("server unavailable: %s\n  → Is hospitusd running? Check with 'doas service hospitus status'", msg)
	default:
		return fmt.Errorf("API error (%d): %s", statusCode, msg)
	}
}

// sendWithRateLimitRetry performs a request, repeating it while the daemon
// answers 429.
//
// The rate limiter replies before the handler runs, so a 429 means the request
// had no effect and repeating it is safe whatever the method. Without this,
// anything driving the API in sequence failed on throughput alone.
//
// newRequest is called once per attempt: a request body can only be read once.
func (c *Client) sendWithRateLimitRetry(ctx context.Context, client *http.Client, newRequest func() (*http.Request, error)) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		req, err := newRequest()
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}

		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusTooManyRequests || attempt >= rateLimitRetries {
			return resp, nil
		}

		wait := retryAfterDelay(resp.Header.Get("Retry-After"), attempt)
		resp.Body.Close()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
	}
}

// rateLimitRetries is how many times a request rejected by the daemon's rate
// limiter is repeated before the error is reported.
const rateLimitRetries = 4

// retryAfterDelay reads the daemon's Retry-After header, falling back to a
// short backoff. The value is capped so a misconfigured server cannot park the
// CLI for minutes.
func retryAfterDelay(header string, attempt int) time.Duration {
	const maxWait = 5 * time.Second
	if secs, err := strconv.Atoi(strings.TrimSpace(header)); err == nil && secs > 0 {
		wait := time.Duration(secs) * time.Second
		if wait > maxWait {
			return maxWait
		}
		return wait
	}
	// 200ms, 400ms, 800ms, 1.6s
	wait := time.Duration(200*(1<<attempt)) * time.Millisecond
	if wait > maxWait {
		return maxWait
	}
	return wait
}

// doRequestWithTimeout performs an HTTP request with a custom timeout; a
// timeout of 0 means no timeout.
func (c *Client) doRequestWithTimeout(ctx context.Context, method, path string, body, result interface{}, timeout time.Duration) error {
	if err := c.configError(); err != nil {
		return err
	}

	var jsonData []byte
	if body != nil {
		var err error
		jsonData, err = json.Marshal(body)
		if err != nil {
			return fmt.Errorf("failed to marshal request: %w", err)
		}
	}

	// Create a custom client with specified timeout, reusing the configured transport (TLS, etc.)
	client := &http.Client{
		Timeout:       timeout,
		Transport:     c.httpClient.Transport,
		CheckRedirect: c.httpClient.CheckRedirect, // strips the key on a cross-host redirect
	}

	resp, err := c.sendWithRateLimitRetry(ctx, client, func() (*http.Request, error) {
		var reqBody io.Reader
		if jsonData != nil {
			reqBody = bytes.NewReader(jsonData)
		}
		req, reqErr := http.NewRequestWithContext(ctx, method, c.baseURL+path, reqBody)
		if reqErr != nil {
			return nil, reqErr
		}
		if jsonData != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		c.setAuthHeader(req)
		return req, nil
	})
	if err != nil {
		// Provide actionable hints for common connection errors
		errStr := err.Error()
		switch {
		case strings.Contains(errStr, "connection refused"):
			return fmt.Errorf("connection refused to %s\n  → Is hospitusd running? Start with 'doas service hospitus start'\n  → Check the URL with 'hospitus context show'", c.baseURL)
		case strings.Contains(errStr, "no such host"):
			return fmt.Errorf("cannot resolve host in %s\n  → Check the URL with 'hospitus context show'", c.baseURL)
		case strings.Contains(errStr, "certificate"):
			return fmt.Errorf("TLS certificate error: %w\n  → Use --tls-ca to specify a CA cert, or add it to the context with 'hospitus context add ... --tls-ca /path/to/ca.crt'", err)
		case strings.Contains(errStr, "context deadline exceeded"), strings.Contains(errStr, "timeout"):
			return fmt.Errorf("request timed out talking to %s\n  → Increase timeout with HOSPITUS_TIMEOUT=120s or --timeout flag", c.baseURL)
		default:
			return fmt.Errorf("request failed: %w", err)
		}
	}
	defer resp.Body.Close()

	// Read response body with a size cap to prevent OOM from malicious servers
	const maxResponseBytes = 100 << 20 // 100 MB
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	// Check status code
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return APIError(resp.StatusCode, respBody)
	}

	// Decode response if result is provided
	if result != nil && resp.StatusCode != http.StatusNoContent {
		if err := json.Unmarshal(respBody, result); err != nil {
			return fmt.Errorf("failed to decode response: %w", err)
		}
	}

	return nil
}
