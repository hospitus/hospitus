package podman

import (
	"encoding/json"
	"fmt"
	"math"
	"net"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/hospitus/hospitus/pkg/provider"
)

// buildCreateArgs assembles a "podman create" command line from a spec.
//
// Shared by CreateInstance, both clone paths and ImportInstance: each of them
// used to build its own, and the three derived paths emitted only labels and
// limits — a cloned or imported container came back without its networks,
// mounts, published ports, environment or command. The image must already be
// validated, normalized and pulled; this function only assembles arguments.
//
// nanoCPUs and memoryMB are the effective limits the caller resolved; zero
// means "leave it to podman". CPUs are carried in podman's own nano-CPU unit
// rather than whole ones, because "--cpus 0.5" is a limit an int cannot hold
// and truncating it to zero removed the limit entirely.
func buildCreateArgs(name, image string, spec provider.InstanceSpec, extraLabels map[string]string, nanoCPUs, memoryMB int64) ([]string, error) {
	args := []string{"create", "--name", name}

	// Resource limits, as the caller resolved them: a clone can carry limits
	// read back from the source rather than the ones in the spec.
	if nanoCPUs > 0 {
		args = append(args, "--cpus",
			strconv.FormatFloat(float64(nanoCPUs)/nanoCPUsPerCPU, 'f', -1, 64))
	}
	if memoryMB > 0 {
		args = append(args, "--memory", fmt.Sprintf("%dm", memoryMB))
	}

	// Network configuration
	if len(spec.Networks) > 0 {
		for i := range spec.Networks {
			nw := &spec.Networks[i]
			switch nw.Type {
			case provider.NetworkTypeBridge:
				if nw.Bridge != "" {
					// host, ns:<path> and container:<id> are namespace modes, not
					// networks: host puts a rootful container in the host's own
					// network namespace. Only a named podman network is accepted.
					if isPodmanNamespaceMode(nw.Bridge) {
						return nil, fmt.Errorf("network %q is a podman namespace mode, not a bridge network", nw.Bridge)
					}
					args = append(args, "--network", nw.Bridge)
				} else {
					args = append(args, "--network", "bridge")
				}
			case provider.NetworkTypeNone:
				args = append(args, "--network", "none")
			default:
				args = append(args, "--network", "bridge")
			}

			if nw.IPv4 != "" && nw.IPv4 != "dhcp" {
				// Parsed before it becomes a podman argument: the value comes
				// from a manifest, and anything that is not an address was
				// passed straight through to surface as an opaque podman
				// failure — or, with the right shape, as something else.
				addr := strings.Split(nw.IPv4, "/")[0]
				if net.ParseIP(addr) == nil {
					return nil, fmt.Errorf("network address %q is not an IP address", nw.IPv4)
				}
				args = append(args, "--ip", addr)
			}
		}
	}

	// A namespace mode, when the container had one and no network of its own.
	// GetInstanceInfo records it because NetworkSettings.Networks is empty for
	// such a container, and a clone would otherwise come back on the default
	// bridge — with network access the original deliberately did or did not
	// have.
	if len(spec.Networks) == 0 {
		if mode, ok := spec.ProviderConfig["network_mode"].(string); ok && mode != "" {
			if !isPodmanNamespaceMode(mode) {
				return nil, fmt.Errorf("network mode %q is not a podman namespace mode", mode)
			}
			args = append(args, "--network", mode)
		}
	}

	// Sorted so the command line is the same from one run to the next, which
	// is what makes the recorded calls comparable in tests.
	args = append(args, labelArgs(spec.Labels)...)
	args = append(args, labelArgs(extraLabels)...)

	// Add hospitus label for identification
	args = append(args, "--label", "hospitus.managed=true")

	// Volume mounts arrive in two shapes: the CLI's -v strings under "volumes",
	// and a manifest's storage.volumes under "mounts". Both are read, or a mount
	// written one way is dropped in silence.
	for _, volStr := range podmanVolumeStrings(spec.ProviderConfig) {
		if err := checkPodmanVolume(volStr); err != nil {
			return nil, err
		}
		args = append(args, "-v", volStr)
	}

	// Port mappings from provider config, in their string form. Validate each
	// entry instead of passing it straight to podman: a malformed or non-numeric
	// spec should be rejected with a clear error rather than surfacing as an
	// opaque podman failure.
	if ports, ok := spec.ProviderConfig["ports"].([]interface{}); ok {
		for _, port := range ports {
			portStr, ok := port.(string)
			if !ok {
				// Refused, not skipped: silently dropping the entry published
				// none of the ports the operator asked for and said nothing.
				return nil, fmt.Errorf("invalid port mapping %v: expected a string", port)
			}
			if err := validatePodmanPortSpec(portStr); err != nil {
				return nil, err
			}
			args = append(args, "-p", portStr)
		}
	}

	// Port forwards from UWM manifest (port_forwards format)
	switch portForwards := spec.ProviderConfig["port_forwards"].(type) {
	case []map[string]interface{}:
		for _, pf := range portForwards {
			arg, err := portForwardArg(pf)
			if err != nil {
				return nil, err
			}
			args = append(args, "-p", arg)
		}
	case []interface{}:
		// The same entries after a round trip through JSON, where the slice
		// loses its element type.
		for _, pf := range portForwards {
			pfMap, ok := pf.(map[string]interface{})
			if !ok {
				return nil, fmt.Errorf("invalid port forward %v: expected an object", pf)
			}
			arg, err := portForwardArg(pfMap)
			if err != nil {
				return nil, err
			}
			args = append(args, "-p", arg)
		}
	}

	// Environment variables
	if envVars, ok := spec.ProviderConfig["environment"].(map[string]interface{}); ok {
		// Sorted, like the labels above: map order is random, and a command
		// line that changes between two runs of the same spec is not one a
		// test can assert on.
		keys := make([]string, 0, len(envVars))
		for k := range envVars {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			value, err := envValue(envVars[k])
			if err != nil {
				return nil, fmt.Errorf("environment variable %s: %w", k, err)
			}
			args = append(args, "-e", k+"="+value)
		}
	}

	// Before the image, like every other option. podman reads --entrypoint as
	// a plain command or a JSON array; the array form is the one that survives
	// an entrypoint with arguments.
	if raw, ok := spec.ProviderConfig["entrypoint"].([]interface{}); ok && len(raw) > 0 {
		entrypoint := make([]string, 0, len(raw))
		for _, a := range raw {
			str, ok := a.(string)
			if !ok {
				return nil, fmt.Errorf("invalid entrypoint argument %v: expected a string", a)
			}
			if !utf8.ValidString(str) {
				return nil, fmt.Errorf("entrypoint argument is not valid UTF-8")
			}
			entrypoint = append(entrypoint, str)
		}
		encoded, err := json.Marshal(entrypoint)
		if err != nil {
			return nil, fmt.Errorf("failed to encode entrypoint: %w", err)
		}
		args = append(args, "--entrypoint", string(encoded))
	}

	// The image the caller already validated, normalized and pulled: this
	// function only assembles arguments.
	args = append(args, image)

	// Command (optional)
	// Podman passes command arguments directly as argv[] to the container
	// via exec.CommandContext. No shell interpretation occurs on the host,
	// so shell metacharacters (' > & etc.) are safe in this context.
	// We only enforce UTF-8 validity.
	if cmd, ok := spec.ProviderConfig["command"].([]interface{}); ok {
		for _, arg := range cmd {
			argStr, ok := arg.(string)
			if !ok {
				// Refused, not skipped: dropping an argument gives the
				// container a different command line than the one asked for.
				return nil, fmt.Errorf("invalid command argument %v: expected a string", arg)
			}
			if !utf8.ValidString(argStr) {
				return nil, fmt.Errorf("command argument is not valid UTF-8")
			}
			args = append(args, argStr)
		}
	}

	return args, nil
}

func labelArgs(labels map[string]string) []string {
	if len(labels) == 0 {
		return nil
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	args := make([]string, 0, len(keys)*2)
	for _, k := range keys {
		args = append(args, "--label", fmt.Sprintf("%s=%s", k, labels[k]))
	}
	return args
}

// portNumber reads a port out of a manifest or JSON value. TOML gives an int,
// JSON a float64, and a decoder configured for it a json.Number; anything else
// is a malformed forward, not a zero.
func portNumber(v interface{}) (int, error) {
	switch n := v.(type) {
	case nil:
		return 0, nil
	case int:
		return n, nil
	case int64:
		return int(n), nil
	case float64:
		if n != math.Trunc(n) {
			return 0, fmt.Errorf("port %v is not a whole number", v)
		}
		return int(n), nil
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return 0, fmt.Errorf("port %v is not a whole number", v)
		}
		return int(i), nil
	default:
		return 0, fmt.Errorf("port %v is not a number", v)
	}
}

// checkPortRange rejects a port podman cannot publish. A zero used to mean
// "entry absent" and skipped the forward in silence; the callers ask for the
// range explicitly instead.
func checkPortRange(port int) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("port %d is out of range (1-65535)", port)
	}
	return nil
}

// envValue renders an environment value for a "-e NAME=value" argument.
//
// %v was doing this, and it renders a float64 in whatever form it likes:
// a JSON 1e21 reached podman as "1e+21" and 0.30000000000000004 as itself.
// The numeric shapes a decoder actually produces are formatted explicitly, and
// anything without an obvious textual form is refused rather than guessed at.
func envValue(v interface{}) (string, error) {
	switch n := v.(type) {
	case string:
		return n, nil
	case bool:
		return strconv.FormatBool(n), nil
	case int:
		return strconv.Itoa(n), nil
	case int64:
		return strconv.FormatInt(n, 10), nil
	case float64:
		if n == math.Trunc(n) && math.Abs(n) < 1<<53 {
			return strconv.FormatInt(int64(n), 10), nil
		}
		return strconv.FormatFloat(n, 'f', -1, 64), nil
	case json.Number:
		return n.String(), nil
	default:
		return "", fmt.Errorf("value %v has no environment representation", v)
	}
}

// portForwardArg renders one manifest port forward as a "-p" value.
//
// Shared by both shapes port_forwards arrives in: they had drifted, and the
// typed one read its ports with a plain int assertion that turned a float64 or
// a json.Number into a zero and dropped the forward without a word.
func portForwardArg(pf map[string]interface{}) (string, error) {
	hostPort, err := portNumber(pf["host"])
	if err == nil {
		err = checkPortRange(hostPort)
	}
	if err != nil {
		return "", fmt.Errorf("port forward host: %w", err)
	}

	containerPort, err := portNumber(pf["container"])
	if err == nil {
		err = checkPortRange(containerPort)
	}
	if err != nil {
		return "", fmt.Errorf("port forward container: %w", err)
	}

	protocol := "tcp"
	if v, present := pf["protocol"]; present && v != nil {
		raw, ok := v.(string)
		if !ok {
			return "", fmt.Errorf("port forward protocol %v: expected a string", v)
		}
		if raw != "" {
			normalized, perr := normalizePortProtocol(raw)
			if perr != nil {
				return "", perr
			}
			protocol = normalized
		}
	}

	return fmt.Sprintf("%d:%d/%s", hostPort, containerPort, protocol), nil
}

// nanoCPUsPerCPU is podman's unit for --cpus: one CPU is 1e9 nano-CPUs.
const nanoCPUsPerCPU = 1e9
