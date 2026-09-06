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
	"strconv"
	"strings"
)

// ExecRequest contains parameters for executing a command in an instance
type ExecRequest struct {
	Command    string            `json:"command"`
	Args       []string          `json:"args,omitempty"`
	User       string            `json:"user,omitempty"`
	WorkingDir string            `json:"working_dir,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
	Timeout    int               `json:"timeout,omitempty"`
}

// ExecResult contains the result of command execution
type ExecResult struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout,omitempty"`
	Stderr   string `json:"stderr,omitempty"`
}

// ExecCommand executes a command inside an instance via the API
// This operation has no timeout as commands can run for extended periods
func (c *Client) ExecCommand(ctx context.Context, instanceID string, req ExecRequest) (*ExecResult, error) {
	path := fmt.Sprintf("/api/v1/instances/%s/exec", url.PathEscape(instanceID))

	var result ExecResult
	// Use no timeout (0) as exec commands like pkg bootstrap can take a long time
	if err := c.doRequestWithTimeout(ctx, "POST", path, req, &result, 0); err != nil {
		return nil, err
	}

	return &result, nil
}

// ExecCommandStream executes a command and streams output to the provided writers.
// Returns the exit code when the command completes.
func (c *Client) ExecCommandStream(ctx context.Context, instanceID string, req ExecRequest, stdout, stderr io.Writer) (int, error) {
	if err := c.configError(); err != nil {
		return -1, err
	}
	path := fmt.Sprintf("/api/v1/instances/%s/exec", url.PathEscape(instanceID))

	// Prepare request body
	jsonData, err := json.Marshal(req)
	if err != nil {
		return -1, fmt.Errorf("failed to marshal request: %w", err)
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
		return -1, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	// Check for error status codes
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return -1, APIError(resp.StatusCode, body)
	}

	// Use buffered reader for efficient reading
	reader := bufio.NewReader(resp.Body)
	exitCode := 0
	var lineBuffer bytes.Buffer

	for {
		// Read a chunk of data
		chunk := make([]byte, 4096)
		n, err := reader.Read(chunk)
		if n > 0 {
			// Process the chunk
			data := chunk[:n]

			// Add to line buffer and check for complete lines
			lineBuffer.Write(data)

			// Process complete lines from buffer
			for {
				line, lineErr := lineBuffer.ReadString('\n')
				if lineErr != nil {
					// No complete line yet, put back what we read
					lineBuffer.WriteString(line)
					break
				}

				// Remove trailing newline
				line = strings.TrimSuffix(line, "\n")
				line = strings.TrimSuffix(line, "\r")

				// Check for exit code marker
				if strings.HasPrefix(line, "EXIT_CODE: ") {
					codeStr := strings.TrimPrefix(line, "EXIT_CODE: ")
					if code, parseErr := strconv.Atoi(codeStr); parseErr == nil {
						exitCode = code
					}
					continue
				}

				// Check for error
				if strings.HasPrefix(line, "ERROR: ") {
					fmt.Fprintln(stderr, strings.TrimPrefix(line, "ERROR: "))
					continue
				}

				// Write output to stdout (with newline)
				fmt.Fprintln(stdout, line)
			}
		}

		if err != nil {
			// Handle EOF and unexpected EOF similarly - connection closed, process remaining data
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || strings.Contains(err.Error(), "EOF") {
				// Process any remaining data in buffer
				remaining := lineBuffer.String()
				if remaining != "" {
					// Check for exit code in remaining data
					switch {
					case strings.HasPrefix(remaining, "EXIT_CODE: "):
						codeStr := strings.TrimPrefix(remaining, "EXIT_CODE: ")
						codeStr = strings.TrimSpace(codeStr)
						if code, parseErr := strconv.Atoi(codeStr); parseErr == nil {
							exitCode = code
						}
					case strings.HasPrefix(remaining, "ERROR: "):
						fmt.Fprintln(stderr, strings.TrimPrefix(remaining, "ERROR: "))
					case strings.TrimSpace(remaining) != "":
						fmt.Fprint(stdout, remaining)
					}
				}
				// For unexpected EOF, return the exit code we have (default 0)
				// This handles cases where the server closes connection before sending EXIT_CODE
				break
			}
			return exitCode, fmt.Errorf("failed to read stream: %w", err)
		}
	}

	return exitCode, nil
}
