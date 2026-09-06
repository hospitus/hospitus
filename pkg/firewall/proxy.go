package firewall

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/hospitus/hospitus/pkg/logging"
)

// acceptErrorBackoff is the pause applied after an unexpected Accept error to
// avoid a 100% CPU busy-loop when the listener keeps returning errors.
const acceptErrorBackoff = 100 * time.Millisecond

// TCPProxy manages TCP proxies for localhost port forwarding.
// This is needed because PF rdr rules on BSD don't work for localhost traffic
// since the kernel handles local connections before packets reach PF.
type TCPProxy struct {
	mu      sync.RWMutex
	proxies map[string]*proxyInstance
	logger  *slog.Logger
}

// proxyInstance represents a single TCP proxy listening on a port
type proxyInstance struct {
	listener   net.Listener
	targetIP   string
	targetPort int
	cancel     context.CancelFunc
	wg         sync.WaitGroup
}

// NewTCPProxy creates a new TCP proxy manager
func NewTCPProxy() *TCPProxy {
	return &TCPProxy{
		proxies: make(map[string]*proxyInstance),
		logger:  logging.WithComponent("firewall-proxy"),
	}
}

// proxyKey generates a unique key for a proxy
func proxyKey(hostPort int, protocol string) string {
	return fmt.Sprintf("%s-%d", protocol, hostPort)
}

// Start starts a TCP proxy for the given mapping
func (p *TCPProxy) Start(mapping PortMapping) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	key := proxyKey(mapping.HostPort, string(mapping.Protocol))

	// Check if already running
	if _, exists := p.proxies[key]; exists {
		return nil // Already running
	}

	// Only handle TCP for now
	if mapping.Protocol != ProtocolTCP {
		return nil
	}

	// Listen on localhost only
	listenAddr := fmt.Sprintf("127.0.0.1:%d", mapping.HostPort)
	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", listenAddr, err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	instance := &proxyInstance{
		listener:   listener,
		targetIP:   mapping.TargetIP,
		targetPort: mapping.TargetPort,
		cancel:     cancel,
	}

	p.proxies[key] = instance

	// Start accepting connections
	instance.wg.Add(1)
	go func() {
		defer instance.wg.Done()
		p.acceptLoop(ctx, instance)
	}()

	return nil
}

// Stop stops the TCP proxy for the given mapping
func (p *TCPProxy) Stop(mapping PortMapping) error {
	key := proxyKey(mapping.HostPort, string(mapping.Protocol))

	// Remove the entry and close the listener under the lock, then wait for
	// in-flight goroutines to drain WITHOUT holding the lock, so a slow
	// connection cannot block other proxy operations.
	p.mu.Lock()
	instance, exists := p.proxies[key]
	if !exists {
		p.mu.Unlock()
		return nil
	}
	delete(p.proxies, key)
	instance.cancel()
	_ = instance.listener.Close()
	p.mu.Unlock()

	instance.wg.Wait()
	return nil
}

// StopAll stops all proxies
func (p *TCPProxy) StopAll() {
	// Detach all instances under the lock, then close/drain them outside the
	// lock so wg.Wait() never blocks concurrent proxy operations.
	p.mu.Lock()
	instances := make([]*proxyInstance, 0, len(p.proxies))
	for key, instance := range p.proxies {
		instance.cancel()
		_ = instance.listener.Close()
		instances = append(instances, instance)
		delete(p.proxies, key)
	}
	p.mu.Unlock()

	for _, instance := range instances {
		instance.wg.Wait()
	}
}

// acceptLoop accepts connections and forwards them
func (p *TCPProxy) acceptLoop(ctx context.Context, instance *proxyInstance) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Set deadline to allow checking context periodically
		if tcpListener, ok := instance.listener.(*net.TCPListener); ok {
			_ = tcpListener.SetDeadline(time.Now().Add(1 * time.Second))
		}

		conn, err := instance.listener.Accept()
		if err != nil {
			// Check if context is canceled
			select {
			case <-ctx.Done():
				return
			default:
			}
			// Timeout is expected (we set a deadline to poll ctx), continue
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				continue
			}
			// Unexpected error: log it and back off before retrying so a
			// persistently-failing listener cannot spin at 100% CPU.
			p.logger.Warn("proxy accept error", "target_ip", instance.targetIP,
				"target_port", instance.targetPort, logging.FieldError, err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(acceptErrorBackoff):
			}
			continue
		}

		// Handle connection in a goroutine
		instance.wg.Add(1)
		go func(clientConn net.Conn) {
			defer instance.wg.Done()
			defer clientConn.Close()
			p.handleConnection(ctx, clientConn, instance.targetIP, instance.targetPort)
		}(conn)
	}
}

// handleConnection handles a single proxied connection
func (p *TCPProxy) handleConnection(ctx context.Context, clientConn net.Conn, targetIP string, targetPort int) {
	// JoinHostPort, not Sprintf: an IPv6 literal needs its brackets, and
	// Validate accepts one, so "%s:%d" produces an address Dial cannot parse.
	targetAddr := net.JoinHostPort(targetIP, strconv.Itoa(targetPort))

	dialer := net.Dialer{
		Timeout: 5 * time.Second,
	}

	targetConn, err := dialer.DialContext(ctx, "tcp", targetAddr)
	if err != nil {
		// The client connection just closes otherwise, leaving an operator with
		// a forward that does nothing and no way to see why.
		p.logger.Warn("proxy could not reach the target", "target", targetAddr,
			"client", clientConn.RemoteAddr().String(), logging.FieldError, err)
		return
	}
	defer targetConn.Close()

	// Create a context that cancels when either connection closes
	connCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Copy data bidirectionally
	var wg sync.WaitGroup
	wg.Add(2)

	// Client -> Target
	go func() {
		defer wg.Done()
		_, _ = io.Copy(targetConn, clientConn)
		// Signal the other direction to stop
		if tcpConn, ok := targetConn.(*net.TCPConn); ok {
			_ = tcpConn.CloseWrite()
		}
	}()

	// Target -> Client
	go func() {
		defer wg.Done()
		_, _ = io.Copy(clientConn, targetConn)
		// Signal the other direction to stop
		if tcpConn, ok := clientConn.(*net.TCPConn); ok {
			_ = tcpConn.CloseWrite()
		}
	}()

	// Wait for copy operations to finish or context cancellation
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-connCtx.Done():
	}
}

// IsRunning checks if a proxy is running for the given mapping
func (p *TCPProxy) IsRunning(hostPort int, protocol string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()

	key := proxyKey(hostPort, protocol)
	_, exists := p.proxies[key]
	return exists
}
