package api

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/hospitus/hospitus/internal/datastore"
	"github.com/hospitus/hospitus/internal/security"
	"github.com/hospitus/hospitus/pkg/logging"
	"github.com/hospitus/hospitus/pkg/provider"
)

// handleExec executes a command inside an instance.
//
// POST /api/v1/instances/{id}/exec
//
// Request body:
//
//	{
//	  "command": "/bin/ls",
//	  "args": ["-la", "/var"],
//	  "user": "root",
//	  "timeout": 30
//	}
//
// Response (JSON mode):
//
//	{
//	  "exit_code": 0,
//	  "stdout": "...",
//	  "stderr": ""
//	}
//
// Response (Streaming mode - Accept: text/plain):
// Streams stdout/stderr in real-time, ends with "EXIT_CODE: N"
func (s *Server) handleExec(w http.ResponseWriter, r *http.Request, instanceID string) {
	ctx := r.Context()

	// Disable write deadline for exec operations (commands like pkg install can take minutes)
	// This applies to both streaming and non-streaming modes
	rc := http.NewResponseController(w)
	if err := rc.SetWriteDeadline(time.Time{}); err != nil {
		// Log but continue - some ResponseWriters may not support this
		s.logger.Warn("Failed to disable write deadline for exec request", logging.FieldInstance, instanceID, logging.FieldError, err)
	}

	// Get instance from datastore
	instance, err := s.lookupInstance(ctx, instanceID)
	if err != nil {
		s.writeLoggedError(w, http.StatusNotFound, "Instance not found", err)
		return
	}

	prov, err := s.registry.Get(instance.Provider)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, fmt.Sprintf("Provider not found: %s", instance.Provider))
		return
	}

	// Check if provider supports exec
	execProv, ok := prov.(provider.ExecProvider)
	if !ok {
		s.writeError(w, http.StatusNotImplemented, fmt.Sprintf("Provider %s does not support exec", instance.Provider))
		return
	}

	// Parse request body
	var req provider.ExecOptions
	if err := s.decodeJSONBody(r, &req); err != nil {
		s.writeLoggedError(w, http.StatusBadRequest, "Invalid request body", err)
		return
	}

	// Validate command
	if req.Command == "" {
		s.writeError(w, http.StatusBadRequest, "Command is required")
		return
	}

	// Check if client wants streaming (Accept: text/plain)
	acceptHeader := r.Header.Get("Accept")
	wantsStreaming := strings.Contains(acceptHeader, "text/plain")

	// Audit the exec access for BOTH streaming and non-streaming paths, before
	// dispatching. The streaming branch returns from inside the dispatch, so an
	// audit placed after it records nothing for a streaming exec — which the
	// Accept header alone selects.
	clientIP := s.extractClientIP(r)
	// SECURITY: Never accept API keys from URL query parameters.
	// Keys in URLs are logged by proxies, routers, and servers.
	apiKeyHash := hashAPIKey(r.Header.Get("X-API-Key"))
	security.GetGlobalAuditLogger().LogResourceAccess(clientIP, apiKeyHash, "exec", fmt.Sprintf("instance:%s command:%s", instance.Name, req.Command))

	// Check if provider supports streaming and client wants it
	streamProv, supportsStreaming := prov.(provider.ExecStreamingProvider)
	if wantsStreaming && supportsStreaming {
		// Streaming mode
		s.handleExecStreaming(w, r, instance, streamProv, req)
		return
	}

	// Non-streaming mode (original behavior)
	result, err := execProv.ExecCommand(ctx, instance.Handle, req)
	if err != nil {
		// If the error indicates the jail is not running but the datastore thinks it is, resync state
		if strings.Contains(err.Error(), "is not running") && instance.State == provider.StateRunning {
			s.logger.Warn("Instance is not running but datastore thinks it is; resyncing state", logging.FieldInstance, instance.ID)
			if updateErr := s.datastore.UpdateInstanceState(ctx, instance.ID, provider.StateStopped); updateErr != nil {
				s.logger.Error("Failed to update instance state during resync", logging.FieldInstance, instance.ID, logging.FieldError, updateErr)
			}
		}

		security.GetGlobalAuditLogger().LogSuspiciousActivity(clientIP, fmt.Sprintf("exec command failed: %s", req.Command), map[string]interface{}{
			"instance": instance.Name,
			"command":  req.Command,
			"error":    err.Error(),
		})
		s.writeLoggedError(w, http.StatusInternalServerError, "Exec failed", err)
		return
	}

	// Log successful exec command
	// Fire-and-forget audit logging; failures must not block the request.
	_ = security.GetGlobalAuditLogger().Log(&security.AuditEvent{
		Timestamp:  time.Now(),
		EventType:  security.EventResourceAccessed,
		Severity:   security.SeverityInfo,
		ClientIP:   clientIP,
		APIKeyHash: apiKeyHash,
		Action:     "exec_success",
		Resource:   fmt.Sprintf("instance:%s", instance.Name),
		Message:    fmt.Sprintf("Exec command completed: %s", req.Command),
		Details: map[string]interface{}{
			"command":    req.Command,
			"exit_code":  result.ExitCode,
			"stdout_len": len(result.Stdout),
			"stderr_len": len(result.Stderr),
		},
	})

	s.writeJSON(w, http.StatusOK, result)
}

// handleExecStreaming handles streaming exec requests
func (s *Server) handleExecStreaming(w http.ResponseWriter, r *http.Request, instance *datastore.Instance, streamProv provider.ExecStreamingProvider, req provider.ExecOptions) {
	ctx := r.Context()

	// Disable write deadline for streaming (commands like pkg install can take minutes)
	// http.ResponseController is available in Go 1.20+
	rc := http.NewResponseController(w)
	if err := rc.SetWriteDeadline(time.Time{}); err != nil {
		// Log but continue - some ResponseWriters may not support this
		s.logger.Warn("Failed to disable write deadline for streaming exec request", logging.FieldInstance, instance.Name, logging.FieldProvider, instance.Provider, logging.FieldError, err)
	}

	// Set headers for streaming
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)

	// Get flusher for real-time output
	flusher, ok := w.(http.Flusher)

	// Flush headers immediately so the client doesn't timeout waiting for
	// response headers while a silent command executes (e.g., sleep loops).
	// Without this, Go's HTTP server buffers headers until the first body Write(),
	// which can cause ResponseHeaderTimeout on the client side.
	if ok {
		flusher.Flush()
	}
	if !ok {
		// Fallback if flusher not available
		fmt.Fprintln(w, "ERROR: Streaming not supported")
		return
	}

	// Create a flushing writer that flushes after each write
	flushWriter := newFlushingWriter(w, flusher)

	// Execute command with streaming
	exitCode, err := streamProv.ExecCommandStream(ctx, instance.Handle, req, flushWriter, flushWriter)

	// Write exit code at the end
	if err != nil {
		// If the error indicates the jail is not running but the datastore thinks it is, resync state
		if strings.Contains(err.Error(), "is not running") && instance.State == provider.StateRunning {
			s.logger.Warn("Instance is not running but datastore thinks it is (streaming); resyncing state", logging.FieldInstance, instance.ID)
			if updateErr := s.datastore.UpdateInstanceState(ctx, instance.ID, provider.StateStopped); updateErr != nil {
				s.logger.Error("Failed to update instance state during streaming resync", logging.FieldInstance, instance.ID, logging.FieldError, updateErr)
			}
		}

		flushWriter.startLine()
		fmt.Fprintf(w, "ERROR: %v\n", err)
		fmt.Fprintln(w, "EXIT_CODE: -1")
	} else {
		flushWriter.startLine()
		fmt.Fprintf(w, "EXIT_CODE: %d\n", exitCode)
	}
	flusher.Flush()
}

// flushingWriter wraps a writer and flushes after each write.
//
// It also remembers whether the last byte it passed on was a newline, because
// the exit-code marker that follows the command's output has to start a line of
// its own. Output that does not end in a newline would otherwise run straight
// into it, and the client — which strips the marker line by line — would hand
// the caller "the quick brown foxEXIT_CODE: 0".
type flushingWriter struct {
	w io.Writer
	f http.Flusher
	// endsWithNewline starts true so that a command producing no output at all
	// does not get a blank line before its marker.
	endsWithNewline bool
}

func newFlushingWriter(w io.Writer, f http.Flusher) *flushingWriter {
	return &flushingWriter{w: w, f: f, endsWithNewline: true}
}

func (fw *flushingWriter) Write(p []byte) (int, error) {
	n, err := fw.w.Write(p)
	if n > 0 {
		fw.endsWithNewline = p[n-1] == '\n'
	}
	if err == nil {
		fw.f.Flush()
	}
	return n, err
}

// startLine writes a newline unless one has just been written, so what follows
// begins a line of its own.
func (fw *flushingWriter) startLine() {
	if !fw.endsWithNewline {
		fmt.Fprintln(fw.w)
	}
}

// handleConsoleInfo returns console information for an instance.
//
// GET /api/v1/instances/{id}/console
//
// Response:
//
//	{
//	  "available": true,
//	  "command": "hospitus jail console myjail",
//	  "state": "running"
//	}
func (s *Server) handleConsoleInfo(w http.ResponseWriter, r *http.Request, instanceID string) {
	ctx := r.Context()

	// Get instance from datastore
	instance, err := s.lookupInstance(ctx, instanceID)
	if err != nil {
		s.writeLoggedError(w, http.StatusNotFound, "Instance not found", err)
		return
	}

	prov, err := s.registry.Get(instance.Provider)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, fmt.Sprintf("Provider not found: %s", instance.Provider))
		return
	}

	// Check if provider supports console
	_, ok := prov.(provider.ConsoleProvider)
	if !ok {
		s.writeError(w, http.StatusNotImplemented, fmt.Sprintf("Provider %s does not support console", instance.Provider))
		return
	}

	// Check if instance is running
	state, err := prov.GetInstanceState(ctx, instance.Handle)
	if err != nil {
		s.writeLoggedError(w, http.StatusInternalServerError, "Failed to get instance state", err)
		return
	}

	// Build console info response
	available := state == provider.StateRunning
	command := ""

	switch instance.Provider {
	case "jail":
		command = fmt.Sprintf("hospitus jail console %s", instance.Name)
	case "bhyve":
		command = fmt.Sprintf("hospitus bhyve console %s", instance.Name)
	case "qemu":
		command = fmt.Sprintf("hospitus qemu console %s", instance.Name)
	}

	response := map[string]interface{}{
		"available": available,
		"command":   command,
		"state":     state,
	}

	s.writeJSON(w, http.StatusOK, response)
}
