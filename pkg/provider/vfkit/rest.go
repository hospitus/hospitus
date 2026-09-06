package vfkit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"
)

// restClient talks to a running VM over the control socket vfkit exposes with
// --restful-uri.
//
// The socket is a Unix socket rather than a TCP port: a port would have to be
// allocated per VM, tracked, and would be reachable by anything on the host.
type restClient struct {
	http *http.Client
}

func newRESTClient() *restClient {
	return &restClient{
		http: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// vmState is what GET /vm/state returns.
type vmState struct {
	State       string `json:"state"`
	CanStart    bool   `json:"canStart"`
	CanPause    bool   `json:"canPause"`
	CanResume   bool   `json:"canResume"`
	CanStop     bool   `json:"canStop"`
	CanHardStop bool   `json:"canHardStop"`
}

// vmInspect is what GET /vm/inspect returns. Devices are left opaque: nothing
// here needs to interpret them, and the shape belongs to vfkit.
//
// The field names come from what vfkit 0.6 actually sends, which is not what its
// documentation shows: "vcpus" and "memoryBytes" rather than "cpus" and
// "memory". Decoding the documented names silently yielded zeros.
type vmInspect struct {
	CPUs        uint   `json:"vcpus"`
	MemoryBytes uint64 `json:"memoryBytes"`
}

// clientFor builds an HTTP client bound to one VM's socket. The host part of the
// URL is ignored by the dialer but has to be present for the request to parse.
func (c *restClient) clientFor(socketPath string) *http.Client {
	return &http.Client{
		Timeout: c.http.Timeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socketPath)
			},
		},
	}
}

// State reads the VM's current state.
func (c *restClient) State(ctx context.Context, socketPath string) (*vmState, error) {
	var state vmState
	if err := c.get(ctx, socketPath, "/vm/state", &state); err != nil {
		return nil, err
	}
	return &state, nil
}

// Inspect reads the VM's resource configuration.
func (c *restClient) Inspect(ctx context.Context, socketPath string) (*vmInspect, error) {
	var inspect vmInspect
	if err := c.get(ctx, socketPath, "/vm/inspect", &inspect); err != nil {
		return nil, err
	}
	return &inspect, nil
}

// SetState asks the VM to change state. Valid values are Stop, HardStop, Pause
// and Resume.
func (c *restClient) SetState(ctx context.Context, socketPath, state string) error {
	body, err := json.Marshal(map[string]string{"state": state})
	if err != nil {
		return fmt.Errorf("failed to encode state request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"http://vfkit/vm/state", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to build state request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.clientFor(socketPath).Do(req)
	if err != nil {
		return fmt.Errorf("failed to reach the VM control socket: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("vfkit refused state %q: HTTP %d", state, resp.StatusCode)
	}
	return nil
}

func (c *restClient) get(ctx context.Context, socketPath, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://vfkit"+path, http.NoBody)
	if err != nil {
		return fmt.Errorf("failed to build request: %w", err)
	}

	resp, err := c.clientFor(socketPath).Do(req)
	if err != nil {
		return fmt.Errorf("failed to reach the VM control socket: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("vfkit returned HTTP %d for %s", resp.StatusCode, path)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("failed to decode %s: %w", path, err)
	}
	return nil
}
