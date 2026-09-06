package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/hospitus/hospitus/internal/datastore"
	"github.com/hospitus/hospitus/pkg/provider"
)

// CreateInstanceRequest contains parameters for creating an instance
type CreateInstanceRequest struct {
	Provider string                `json:"-"`
	Spec     provider.InstanceSpec `json:"spec"`
}

// CreateInstance creates a new instance with streaming progress
func (c *Client) CreateInstance(ctx context.Context, req CreateInstanceRequest) (*datastore.Instance, error) {
	if err := c.configError(); err != nil {
		return nil, err
	}
	path := fmt.Sprintf("/api/v1/instances?provider=%s", url.QueryEscape(req.Provider))

	// Prepare request body
	jsonData, err := json.Marshal(req.Spec)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Create client with streaming timeout, inheriting TLS and redirect handling
	client := c.newStreamingClient()

	// Execute request, retrying while the daemon's rate limiter turns it away.
	resp, err := c.sendWithRateLimitRetry(ctx, client, func() (*http.Request, error) {
		httpReq, reqErr := http.NewRequestWithContext(ctx, "POST", c.baseURL+path, bytes.NewReader(jsonData))
		if reqErr != nil {
			return nil, reqErr
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "text/plain") // Request streaming response
		c.setAuthHeader(httpReq)
		return httpReq, nil
	})
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return nil, APIError(resp.StatusCode, body)
	}

	// Read streamed response line by line
	scanner := bufio.NewScanner(resp.Body)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	var instanceJSON string

	for scanner.Scan() {
		line := scanner.Text()

		if strings.HasPrefix(line, "ERROR: ") {
			return nil, fmt.Errorf("%s", strings.TrimPrefix(line, "ERROR: "))
		} else if strings.HasPrefix(line, "SUCCESS: ") {
			// Extract JSON from success line
			instanceJSON = strings.TrimPrefix(line, "SUCCESS: ")
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	// Parse instance from JSON
	if instanceJSON == "" {
		return nil, fmt.Errorf("no instance data received")
	}

	var instance datastore.Instance
	if err := json.Unmarshal([]byte(instanceJSON), &instance); err != nil {
		return nil, fmt.Errorf("failed to parse instance: %w", err)
	}

	return &instance, nil
}

// ListInstancesFilter contains filter parameters for listing instances
type ListInstancesFilter struct {
	Provider string
	State    string
	Labels   map[string]string
}

// ListInstances returns all instances matching the filter
func (c *Client) ListInstances(ctx context.Context, filter ListInstancesFilter) ([]*datastore.Instance, error) {
	query := url.Values{}
	if filter.Provider != "" {
		query.Set("provider", filter.Provider)
	}
	if filter.State != "" {
		query.Set("state", filter.State)
	}
	// The server parses repeated label=key=value parameters
	// (see handleListInstances), not label.<key>=<value>.
	for k, v := range filter.Labels {
		query.Add("label", k+"="+v)
	}

	path := "/api/v1/instances"
	if len(query) > 0 {
		path += "?" + query.Encode()
	}

	var instances []*datastore.Instance
	if err := c.doRequest(ctx, "GET", path, nil, &instances); err != nil {
		return nil, err
	}
	return instances, nil
}

// GetInstance returns details about a specific instance
func (c *Client) GetInstance(ctx context.Context, id string) (*datastore.Instance, error) {
	var instance datastore.Instance
	path := fmt.Sprintf("/api/v1/instances/%s", url.PathEscape(id))
	if err := c.doRequest(ctx, "GET", path, nil, &instance); err != nil {
		return nil, err
	}
	return &instance, nil
}

// UpdateInstanceRequest contains parameters for updating an instance
type UpdateInstanceRequest struct {
	Spec           *provider.InstanceSpec `json:"spec,omitempty"`
	ProviderConfig map[string]interface{} `json:"provider_config,omitempty"`
}

// UpdateInstance updates an instance's configuration
func (c *Client) UpdateInstance(ctx context.Context, id string, req UpdateInstanceRequest) (*datastore.Instance, error) {
	var instance datastore.Instance
	path := fmt.Sprintf("/api/v1/instances/%s", url.PathEscape(id))
	if err := c.doRequest(ctx, "PATCH", path, req, &instance); err != nil {
		return nil, err
	}
	return &instance, nil
}

// DeleteInstance deletes an instance
func (c *Client) DeleteInstance(ctx context.Context, id string, force bool) error {
	path := fmt.Sprintf("/api/v1/instances/%s", url.PathEscape(id))
	if force {
		path += "?force=true"
	}
	// Use streaming timeout since deleting large jails with ZFS datasets can take minutes
	return c.doRequestWithTimeout(ctx, "DELETE", path, nil, nil, StreamingTimeout)
}

// StartInstance starts an instance
func (c *Client) StartInstance(ctx context.Context, id string, w io.Writer) error {
	if err := c.configError(); err != nil {
		return err
	}
	path := fmt.Sprintf("/api/v1/instances/%s/start", url.PathEscape(id))

	// Create HTTP client with streaming timeout, inheriting TLS and redirects
	client := c.newStreamingClient()

	resp, err := c.sendWithRateLimitRetry(ctx, client, func() (*http.Request, error) {
		httpReq, reqErr := http.NewRequestWithContext(ctx, "POST", c.baseURL+path, http.NoBody)
		if reqErr != nil {
			return nil, reqErr
		}
		httpReq.Header.Set("Accept", "text/plain") // Request streaming
		c.setAuthHeader(httpReq)
		return httpReq, nil
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// Handle streaming response (tolerate charset/parameters on the media type).
	//
	// The status counts as much as the type: http.Error also answers
	// text/plain, so a 500 whose body carried no "ERROR: " prefix was scanned
	// as provider output, printed, and reported as a successful start.
	if resp.StatusCode >= 200 && resp.StatusCode < 300 &&
		strings.HasPrefix(resp.Header.Get("Content-Type"), "text/plain") {
		scanner := bufio.NewScanner(resp.Body)
	scan:
		for scanner.Scan() {
			line := scanner.Text()
			switch {
			case strings.HasPrefix(line, "ERROR: "):
				return fmt.Errorf("%s", strings.TrimPrefix(line, "ERROR: "))
			case strings.HasPrefix(line, "SUCCESS: "):
				// Success message, we're done
				break scan
			default:
				// Display provider messages (like RCTL warnings)
				fmt.Fprintln(w, line)
			}
		}

		if err := scanner.Err(); err != nil {
			return fmt.Errorf("error reading response: %w", err)
		}

		return nil
	}

	// Non-streaming fallback (legacy)
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		var errResp struct {
			Error  string `json:"error"`
			Detail string `json:"detail"`
		}
		msg := ""
		if json.Unmarshal(body, &errResp) == nil && errResp.Error != "" {
			msg = errResp.Error
			// Without this the cause is dropped: the daemon answers
			// {"error":"Instance not found","detail":"web"} and the caller was
			// told only "Instance not found", never which instance.
			if errResp.Detail != "" {
				msg += ": " + errResp.Detail
			}
		} else {
			msg = strings.TrimSpace(string(body))
			if msg == "" {
				msg = http.StatusText(resp.StatusCode)
			}
		}
		return fmt.Errorf("API error (%d): %s", resp.StatusCode, msg)
	}

	return nil
}

// StopInstance stops an instance
func (c *Client) StopInstance(ctx context.Context, id string, force bool) error {
	query := url.Values{}
	if force {
		query.Set("force", "true")
	}

	path := fmt.Sprintf("/api/v1/instances/%s/stop", url.PathEscape(id))
	if len(query) > 0 {
		path += "?" + query.Encode()
	}

	// Use streaming timeout since stopping instances (especially jails with shutdown hooks) can take minutes
	return c.doRequestWithTimeout(ctx, "POST", path, nil, nil, StreamingTimeout)
}

// RestartInstance restarts an instance
func (c *Client) RestartInstance(ctx context.Context, id string) error {
	path := fmt.Sprintf("/api/v1/instances/%s/restart", url.PathEscape(id))
	// Use streaming timeout since restarting can take minutes (stop + start)
	return c.doRequestWithTimeout(ctx, "POST", path, nil, nil, StreamingTimeout)
}

// GetEvents returns events for an instance
func (c *Client) GetEvents(ctx context.Context, id string, limit int) ([]datastore.Event, error) {
	query := url.Values{}
	if limit > 0 {
		query.Set("limit", fmt.Sprintf("%d", limit))
	}

	path := fmt.Sprintf("/api/v1/instances/%s/events", url.PathEscape(id))
	if len(query) > 0 {
		path += "?" + query.Encode()
	}

	var events []datastore.Event
	if err := c.doRequest(ctx, "GET", path, nil, &events); err != nil {
		return nil, err
	}
	return events, nil
}

// RenameInstance renames a stopped instance.
func (c *Client) RenameInstance(ctx context.Context, id, newName string) (map[string]string, error) {
	path := fmt.Sprintf("/api/v1/instances/%s/rename", url.PathEscape(id))
	var result map[string]string
	if err := c.doRequest(ctx, "POST", path, map[string]string{"name": newName}, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// UpgradeInstance submits a base-system upgrade job for an instance.
// Returns a map containing the job_id and message.
func (c *Client) UpgradeInstance(ctx context.Context, id, targetRelease string) (map[string]string, error) {
	path := fmt.Sprintf("/api/v1/instances/%s/upgrade", url.PathEscape(id))
	var result map[string]string
	if err := c.doRequest(ctx, "POST", path,
		map[string]string{"target_release": targetRelease},
		&result,
	); err != nil {
		return nil, err
	}
	return result, nil
}

// PauseInstance freezes a running instance (SIGSTOP).
func (c *Client) PauseInstance(ctx context.Context, id string) error {
	path := fmt.Sprintf("/api/v1/instances/%s/pause", url.PathEscape(id))
	return c.doRequest(ctx, "POST", path, nil, nil)
}

// ResumeInstance unfreezes a paused instance (SIGCONT).
func (c *Client) ResumeInstance(ctx context.Context, id string) error {
	path := fmt.Sprintf("/api/v1/instances/%s/resume", url.PathEscape(id))
	return c.doRequest(ctx, "POST", path, nil, nil)
}
