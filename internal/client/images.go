package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// ErrDownloadInterrupted reports a fetch that stopped before the image was
// complete. The partial file stays on the daemon and the same command resumes
// it, but this run has to fail: a script would otherwise go on to use an image
// that is not there.
var ErrDownloadInterrupted = errors.New("image download interrupted before completion")

// FetchImageResult contains the result of a fetch operation
type FetchImageResult struct {
	Message string `json:"message"`
	Status  string `json:"status"`
	Path    string `json:"path"`
}

// FetchImage downloads any catalog image (FreeBSD base systems included).
// It uses the streaming client, so StreamingTimeout applies to the download.
func (c *Client) FetchImage(ctx context.Context, version string, w io.Writer) error {
	if err := c.configError(); err != nil {
		return err
	}
	path := "/api/v1/images/fetch"

	// Prepare request body
	body := map[string]string{"version": version}
	jsonData, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	// Create client with streaming timeout, inheriting TLS and redirect handling
	client := c.newStreamingClient()

	// Execute request, retrying while the daemon's rate limiter turns it away.
	resp, err := c.sendWithRateLimitRetry(ctx, client, func() (*http.Request, error) {
		req, reqErr := http.NewRequestWithContext(ctx, "POST", c.baseURL+path, bytes.NewReader(jsonData))
		if reqErr != nil {
			return nil, reqErr
		}
		req.Header.Set("Content-Type", "application/json")
		c.setAuthHeader(req)
		return req, nil
	})
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return APIError(resp.StatusCode, body)
	}

	// Check Content-Type to determine response format
	contentType := resp.Header.Get("Content-Type")
	if strings.Contains(contentType, "application/json") {
		// JSON response - likely "already_exists" or quick status
		var result FetchImageResult
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return fmt.Errorf("failed to decode response: %w", err)
		}

		if result.Status == "already_exists" {
			fmt.Fprintf(w, "Image already exists: %s\n", result.Path)
			return nil
		}

		if result.Status == "success" || result.Status == "completed" {
			fmt.Fprintln(w, result.Message)
			return nil
		}

		// Unknown status - treat as error if not success
		if result.Status != "" && result.Status != "success" {
			return fmt.Errorf("unexpected status: %s - %s", result.Status, result.Message)
		}
		return nil
	}

	// Read streamed response line by line (text/plain)
	scanner := bufio.NewScanner(resp.Body)
	// Increase buffer size for large lines
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024) // 1MB max token size

	var lastLine string
	var hasProgress bool

	for scanner.Scan() {
		line := scanner.Text()
		lastLine = line

		// Check if it's a JSON response (handles edge cases)
		if strings.HasPrefix(line, "{") && strings.Contains(line, "\"status\"") {
			var result FetchImageResult
			if json.Unmarshal([]byte(line), &result) == nil {
				if result.Status == "already_exists" {
					fmt.Fprintf(w, "Image already exists: %s\n", result.Path)
					return nil
				}
			}
		}

		// Parse and display based on line type
		switch {
		case strings.HasPrefix(line, "PROGRESS: "):
			hasProgress = true
			// Clear line and print progress (carriage return for same-line update)
			fmt.Fprintf(w, "\r%s", strings.TrimPrefix(line, "PROGRESS: "))
		case strings.HasPrefix(line, "COMPLETE: "):
			// Print completion on new line
			fmt.Fprintf(w, "\n%s\n", strings.TrimPrefix(line, "COMPLETE: "))
		case strings.HasPrefix(line, "ERROR: "):
			fmt.Fprintln(w) // New line after progress
			return fmt.Errorf("%s", strings.TrimPrefix(line, "ERROR: "))
		case strings.HasPrefix(line, "SUCCESS: "):
			// Success message already printed by COMPLETE
			continue
		case line == "HEARTBEAT":
			// Ignore heartbeat messages (used to keep connection alive)
			continue
		default:
			// Other messages (like download URL, destination)
			fmt.Fprintln(w, line)
		}
	}

	// Check scanner error
	if err := scanner.Err(); err != nil {
		// If we had progress and got EOF, it might be network issue - don't fail
		// The download can be resumed
		if errors.Is(err, io.EOF) || strings.Contains(err.Error(), "EOF") {
			if hasProgress {
				fmt.Fprintf(w, "\n\nConnection interrupted. Download saved, run the command again to resume.\n")
				return ErrDownloadInterrupted
			}
		}
		return fmt.Errorf("failed to read response: %w", err)
	}

	// Check if we got success
	if !strings.Contains(lastLine, "SUCCESS") && !strings.Contains(lastLine, "COMPLETE") {
		if hasProgress {
			// We had progress but didn't finish - likely interrupted
			fmt.Fprintf(w, "\n\nDownload interrupted. Run the command again to resume.\n")
			return ErrDownloadInterrupted
		}
		return fmt.Errorf("download did not complete successfully")
	}

	return nil
}

// ListImages returns all downloaded images from the daemon's image store.
func (c *Client) ListImages(ctx context.Context) ([]map[string]interface{}, error) {
	var result []map[string]interface{}
	if err := c.doRequest(ctx, "GET", "/api/v1/images", nil, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// DeleteImage deletes a FreeBSD base system image
func (c *Client) DeleteImage(ctx context.Context, imageName string) error {
	path := fmt.Sprintf("/api/v1/images/%s", url.PathEscape(imageName))
	return c.doRequest(ctx, "DELETE", path, nil, nil)
}

// RefreshCatalog triggers a refresh of the image catalog from remote sources
func (c *Client) RefreshCatalog(ctx context.Context) error {
	return c.doRequest(ctx, "POST", "/api/v1/images/refresh", nil, nil)
}
