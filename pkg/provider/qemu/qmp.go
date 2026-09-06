package qemu

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"time"
)

// QMPClient communicates with QEMU via QMP protocol.
type QMPClient struct {
	socketPath string
	conn       net.Conn
	reader     *bufio.Reader
	mu         sync.Mutex
	negotiated bool
}

// QMPResponse represents a QMP response.
type QMPResponse struct {
	Return json.RawMessage `json:"return,omitempty"`
	Error  *QMPError       `json:"error,omitempty"`
	Event  string          `json:"event,omitempty"`
}

// QMPError represents a QMP error.
type QMPError struct {
	Class string `json:"class"`
	Desc  string `json:"desc"`
}

func (e *QMPError) Error() string {
	return fmt.Sprintf("QMP error: %s - %s", e.Class, e.Desc)
}

// QMPGreeting is the initial greeting from QEMU.
type QMPGreeting struct {
	QMP struct {
		Version struct {
			QEMU struct {
				Micro int `json:"micro"`
				Minor int `json:"minor"`
				Major int `json:"major"`
			} `json:"qemu"`
			Package string `json:"package"`
		} `json:"version"`
		Capabilities []string `json:"capabilities"`
	} `json:"QMP"`
}

// NewQMPClient creates a new QMP client for the given socket path.
func NewQMPClient(socketPath string) (*QMPClient, error) {
	return &QMPClient{
		socketPath: socketPath,
	}, nil
}

// Connect establishes connection to QMP socket.
func (c *QMPClient) Connect() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn != nil {
		return nil
	}

	conn, err := net.DialTimeout("unix", c.socketPath, 5*time.Second)
	if err != nil {
		return fmt.Errorf("failed to connect to QMP socket %s: %w", c.socketPath, err)
	}

	c.conn = conn
	c.reader = bufio.NewReader(conn)

	// A VM stopped with SIGSTOP accepts the connection in the kernel but never
	// answers, and the greeting read would otherwise block for good.
	_ = conn.SetDeadline(time.Now().Add(qmpCommandTimeout))
	if err := c.negotiate(); err != nil {
		c.conn.Close()
		c.conn = nil
		return err
	}
	_ = conn.SetDeadline(time.Time{})

	return nil
}

// negotiate performs the QMP capability negotiation.
func (c *QMPClient) negotiate() error {
	// Read greeting
	line, err := c.reader.ReadBytes('\n')
	if err != nil {
		return fmt.Errorf("failed to read QMP greeting: %w", err)
	}

	var greeting QMPGreeting
	if err := json.Unmarshal(line, &greeting); err != nil {
		return fmt.Errorf("failed to parse QMP greeting: %w", err)
	}

	// Send qmp_capabilities to enter command mode
	if _, err := c.conn.Write([]byte(`{"execute": "qmp_capabilities"}` + "\n")); err != nil {
		return fmt.Errorf("failed to send qmp_capabilities: %w", err)
	}

	// Read response
	line, err = c.reader.ReadBytes('\n')
	if err != nil {
		return fmt.Errorf("failed to read qmp_capabilities response: %w", err)
	}

	var resp QMPResponse
	if err := json.Unmarshal(line, &resp); err != nil {
		return fmt.Errorf("failed to parse qmp_capabilities response: %w", err)
	}

	if resp.Error != nil {
		return resp.Error
	}

	c.negotiated = true
	return nil
}

// Close closes the QMP connection.
func (c *QMPClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closeLocked()
}

// closeLocked is Close with the mutex already held, for the callers that are
// inside a critical section when they decide the connection is unusable.
func (c *QMPClient) closeLocked() error {
	if c.conn != nil {
		err := c.conn.Close()
		c.conn = nil
		c.negotiated = false
		return err
	}
	return nil
}

// Execute sends a QMP command and returns the response.
// qmpCommandTimeout bounds a single QMP command exchange. It is a var (not a
// const) so tests can shorten it.
var qmpCommandTimeout = 10 * time.Second

func (c *QMPClient) Execute(command string, args interface{}) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil {
		return nil, fmt.Errorf("QMP not connected")
	}

	// Bound the whole write+read exchange so a hung QEMU cannot block the
	// caller (and the held mutex) indefinitely.
	_ = c.conn.SetDeadline(time.Now().Add(qmpCommandTimeout))
	// Guarded: defers unwind last-registered first, so the close below runs
	// before this one and leaves c.conn nil.
	defer func() {
		if c.conn != nil {
			_ = c.conn.SetDeadline(time.Time{})
		}
	}()

	// A failed exchange leaves the stream out of step. These commands carry no
	// "id", so a late reply to a timed-out command cannot be told apart from
	// the answer to the next one — the connection would start returning the
	// previous command's result. Close it instead and let the caller reconnect.
	failed := true
	defer func() {
		if failed {
			_ = c.closeLocked()
		}
	}()

	// Build command
	cmd := map[string]interface{}{
		"execute": command,
	}
	if args != nil {
		cmd["arguments"] = args
	}

	// Send command
	cmdBytes, err := json.Marshal(cmd)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal QMP command: %w", err)
	}

	if _, err := c.conn.Write(append(cmdBytes, '\n')); err != nil {
		return nil, fmt.Errorf("failed to send QMP command: %w", err)
	}

	// Read response (skip events)
	for {
		line, err := c.reader.ReadBytes('\n')
		if err != nil {
			return nil, fmt.Errorf("failed to read QMP response: %w", err)
		}

		var resp QMPResponse
		if err := json.Unmarshal(line, &resp); err != nil {
			return nil, fmt.Errorf("failed to parse QMP response: %w", err)
		}

		// Skip events
		if resp.Event != "" {
			continue
		}

		if resp.Error != nil {
			// An error object is an in-band reply: the request was sent whole
			// and answered whole, so the stream is still in step. Closing here
			// broke QueryMemory, whose fallback runs a second command on this
			// same client after older QEMU answers CommandNotFound.
			failed = false
			return nil, resp.Error
		}

		failed = false
		return resp.Return, nil
	}
}

// QueryStatus returns the VM status.
func (c *QMPClient) QueryStatus() (*VMStatus, error) {
	result, err := c.Execute("query-status", nil)
	if err != nil {
		return nil, err
	}

	var status VMStatus
	if err := json.Unmarshal(result, &status); err != nil {
		return nil, fmt.Errorf("failed to parse status: %w", err)
	}

	return &status, nil
}

// VMStatus represents QEMU VM status.
type VMStatus struct {
	Running    bool   `json:"running"`
	Singlestep bool   `json:"singlestep"`
	Status     string `json:"status"`
}

// QueryMemory returns memory information.
func (c *QMPClient) QueryMemory() (*MemoryInfo, error) {
	result, err := c.Execute("query-memory-size-summary", nil)
	if err != nil {
		// Fallback for older QEMU versions
		return c.queryMemoryBalloon()
	}

	var mem MemoryInfo
	if err := json.Unmarshal(result, &mem); err != nil {
		return nil, fmt.Errorf("failed to parse memory info: %w", err)
	}

	return &mem, nil
}

func (c *QMPClient) queryMemoryBalloon() (*MemoryInfo, error) {
	result, err := c.Execute("query-balloon", nil)
	if err != nil {
		return nil, err
	}

	var balloon struct {
		Actual int64 `json:"actual"`
	}
	if err := json.Unmarshal(result, &balloon); err != nil {
		return nil, fmt.Errorf("failed to parse balloon info: %w", err)
	}

	return &MemoryInfo{
		BaseMemory: balloon.Actual,
	}, nil
}

// MemoryInfo represents memory information.
type MemoryInfo struct {
	BaseMemory    int64 `json:"base-memory"`
	PluggedMemory int64 `json:"plugged-memory"`
}

// Stop stops the VM (pause).
func (c *QMPClient) Stop() error {
	_, err := c.Execute("stop", nil)
	return err
}

// Continue resumes a paused VM.
func (c *QMPClient) Continue() error {
	_, err := c.Execute("cont", nil)
	return err
}

// SystemPowerdown sends ACPI powerdown signal.
func (c *QMPClient) SystemPowerdown() error {
	_, err := c.Execute("system_powerdown", nil)
	return err
}

// Quit terminates QEMU immediately.
func (c *QMPClient) Quit() error {
	_, err := c.Execute("quit", nil)
	return err
}

// EjectMedia ejects media from a device.
func (c *QMPClient) EjectMedia(device string) error {
	_, err := c.Execute("eject", map[string]interface{}{
		"device": device,
		"force":  true,
	})
	return err
}

// ChangeMedia changes media in a device.
func (c *QMPClient) ChangeMedia(device, target string) error {
	_, err := c.Execute("blockdev-change-medium", map[string]interface{}{
		"device":   device,
		"filename": target,
	})
	return err
}

// AddBlockDevice adds a block device.
func (c *QMPClient) AddBlockDevice(nodeID, driver, filename string) error {
	args := map[string]interface{}{
		"driver":    driver,
		"node-name": nodeID,
		"file": map[string]interface{}{
			"driver":   "file",
			"filename": filename,
		},
	}

	_, err := c.Execute("blockdev-add", args)
	return err
}

// RemoveBlockDevice removes a block device.
func (c *QMPClient) RemoveBlockDevice(nodeID string) error {
	_, err := c.Execute("blockdev-del", map[string]interface{}{
		"node-name": nodeID,
	})
	return err
}

// DeviceAdd adds a device to the guest.
func (c *QMPClient) DeviceAdd(driver string, props map[string]interface{}) error {
	args := map[string]interface{}{
		"driver": driver,
	}
	for k, v := range props {
		args[k] = v
	}

	_, err := c.Execute("device_add", args)
	return err
}

// DeviceDel removes a device from the guest.
func (c *QMPClient) DeviceDel(deviceID string) error {
	_, err := c.Execute("device_del", map[string]interface{}{
		"id": deviceID,
	})
	return err
}

// NetdevAdd adds a network backend.
func (c *QMPClient) NetdevAdd(netdevType, id string, props map[string]interface{}) error {
	args := map[string]interface{}{
		"type": netdevType,
		"id":   id,
	}
	for k, v := range props {
		args[k] = v
	}

	_, err := c.Execute("netdev_add", args)
	return err
}

// NetdevDel removes a network backend.
func (c *QMPClient) NetdevDel(id string) error {
	_, err := c.Execute("netdev_del", map[string]interface{}{
		"id": id,
	})
	return err
}

// QueryBlock returns block device information.
func (c *QMPClient) QueryBlock() ([]BlockInfo, error) {
	result, err := c.Execute("query-block", nil)
	if err != nil {
		return nil, err
	}

	var blocks []BlockInfo
	if err := json.Unmarshal(result, &blocks); err != nil {
		return nil, fmt.Errorf("failed to parse block info: %w", err)
	}

	return blocks, nil
}

// BlockInfo represents block device information.
type BlockInfo struct {
	Device    string `json:"device"`
	Removable bool   `json:"removable"`
	Locked    bool   `json:"locked"`
	Inserted  *struct {
		File   string `json:"file"`
		Driver string `json:"drv"`
	} `json:"inserted,omitempty"`
}

// QueryPCI returns PCI device information.
func (c *QMPClient) QueryPCI() (json.RawMessage, error) {
	return c.Execute("query-pci", nil)
}

// SetBootOrder sets the boot device order using the QEMU HMP boot_set command.
// bootStr is the QEMU boot order string (e.g. "cd" for CD-ROM then disk).
func (c *QMPClient) SetBootOrder(bootStr string) error {
	_, err := c.Execute("human-monitor-command", map[string]interface{}{
		"command-line": fmt.Sprintf("boot_set %s", bootStr),
	})
	return err
}

// GuestNetworkInterface represents a network interface as reported by the
// QEMU guest agent via guest-network-get-interfaces.
type GuestNetworkInterface struct {
	Name            string           `json:"name"`
	HardwareAddress string           `json:"hardware-address,omitempty"`
	IPAddresses     []GuestIPAddress `json:"ip-addresses,omitempty"`
}

// GuestIPAddress is one address the guest agent reports on an interface.
//
// Named rather than anonymous: an external caller — and every test case — had
// to restate the whole struct literal to build one.
type GuestIPAddress struct {
	IPAddressType string `json:"ip-address-type"` // "ipv4" or "ipv6"
	IPAddress     string `json:"ip-address"`
	Prefix        int    `json:"prefix"`
}

// GuestNetworkGetInterfaces returns network interface info from the guest agent.
// Requires qemu-guest-agent to be running inside the VM.
func (c *QMPClient) GuestNetworkGetInterfaces() ([]GuestNetworkInterface, error) {
	result, err := c.Execute("guest-network-get-interfaces", nil)
	if err != nil {
		return nil, fmt.Errorf("guest-network-get-interfaces failed (is qemu-guest-agent running?): %w", err)
	}

	var ifaces []GuestNetworkInterface
	if err := json.Unmarshal(result, &ifaces); err != nil {
		return nil, fmt.Errorf("failed to parse network interfaces: %w", err)
	}

	return ifaces, nil
}

// HostfwdAdd adds a port-forwarding rule to a running VM's user-mode network.
// protocol is "tcp" or "udp", hostPort is the host port, guestPort is the VM port.
// netdevID is typically "net0" (the first user-mode netdev).
func (c *QMPClient) HostfwdAdd(netdevID, protocol string, hostPort, guestPort int) error {
	cmd := fmt.Sprintf("hostfwd_add %s %s::%d-:%d", netdevID, protocol, hostPort, guestPort)
	_, err := c.Execute("human-monitor-command", map[string]interface{}{
		"command-line": cmd,
	})
	return err
}

// HostfwdRemove removes a port-forwarding rule from a running VM's user-mode network.
func (c *QMPClient) HostfwdRemove(netdevID, protocol string, hostPort int) error {
	cmd := fmt.Sprintf("hostfwd_remove %s %s::%d", netdevID, protocol, hostPort)
	_, err := c.Execute("human-monitor-command", map[string]interface{}{
		"command-line": cmd,
	})
	return err
}

// BlockStats represents block device I/O statistics from query-blockstats.
type BlockStats struct {
	Device string       `json:"device"`
	Stats  BlockIOStats `json:"stats"`
}

// BlockIOStats contains the I/O counters for a block device.
type BlockIOStats struct {
	ReadBytes  int64 `json:"rd_bytes"`
	WriteBytes int64 `json:"wr_bytes"`
	ReadOps    int64 `json:"rd_operations"`
	WriteOps   int64 `json:"wr_operations"`
}

// QueryBlockStats returns I/O statistics for all block devices.
func (c *QMPClient) QueryBlockStats() ([]BlockStats, error) {
	result, err := c.Execute("query-blockstats", nil)
	if err != nil {
		return nil, err
	}

	var stats []BlockStats
	if err := json.Unmarshal(result, &stats); err != nil {
		return nil, fmt.Errorf("failed to parse blockstats: %w", err)
	}

	return stats, nil
}

// NetStats represents network device statistics.
type NetStats struct {
	Name    string `json:"name"`
	RxBytes int64  `json:"rx-bytes"`
	RxPkts  int64  `json:"rx-packets"`
	TxBytes int64  `json:"tx-bytes"`
	TxPkts  int64  `json:"tx-packets"`
}

// QueryNetStats returns network I/O statistics by querying the HMP 'info network' command.
// QEMU doesn't have a dedicated QMP command for net stats, so we fall back to
// querying network info best-effort.
func (c *QMPClient) QueryNetStats() ([]NetStats, error) {
	// Use the human-monitor-command to get network info
	result, err := c.Execute("human-monitor-command", map[string]interface{}{
		"command-line": "info network",
	})
	if err != nil {
		return nil, err
	}

	// The HMP output is free-form text, not easily parseable for byte counts.
	// Return empty stats — the caller should try /proc or platform-specific methods.
	_ = result
	return nil, nil
}

// filterGuestIPs extracts non-loopback IPs from a slice of GuestNetworkInterface.
// Loopback interfaces (lo, lo0) and loopback IP addresses are excluded.
func filterGuestIPs(ifaces []GuestNetworkInterface) []net.IP {
	var ips []net.IP
	for _, iface := range ifaces {
		if iface.Name == "lo" || iface.Name == "lo0" {
			continue
		}
		for _, addr := range iface.IPAddresses {
			if ip := net.ParseIP(addr.IPAddress); ip != nil {
				if !ip.IsLoopback() {
					ips = append(ips, ip)
				}
			}
		}
	}
	return ips
}

// queryGuestIPs connects to the QMP socket and retrieves non-loopback IPv4/IPv6
// addresses from all guest network interfaces. Returns nil on any error (best-effort).
func queryGuestIPs(qmpSocket string) []net.IP {
	client, err := NewQMPClient(qmpSocket)
	if err != nil {
		return nil
	}
	if err := client.Connect(); err != nil {
		return nil
	}
	defer client.Close()

	ifaces, err := client.GuestNetworkGetInterfaces()
	if err != nil {
		return nil
	}

	return filterGuestIPs(ifaces)
}
