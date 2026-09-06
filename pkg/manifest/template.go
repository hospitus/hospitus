package manifest

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"text/template"
)

// RenderTemplate processes a raw manifest through Go text/template
func RenderTemplate(raw []byte, vars map[string]any, secrets SecretStore, scope string) ([]byte, error) {
	if vars == nil {
		vars = make(map[string]any)
	}

	// A marker the manifest carries in its own text is put aside before
	// rendering and restored after, so what remains in the output is what
	// rendering produced. Counting the two instead — as this did — missed a
	// missing variable in any document that also removed a literal marker,
	// through an {{ if }} that did not fire.
	source := hideLiteralMarkers(raw)

	tmpl, err := template.New("manifest").
		Funcs(templateFuncs(vars, secrets, scope)).
		Option("missingkey=zero").
		Parse(string(source))
	if err != nil {
		return nil, fmt.Errorf("failed to parse template: %w", err)
	}

	var buf bytes.Buffer
	// For map data, missing keys will return nil or zero value depending on options.
	// Our custom 'default' function handles these.
	if err := tmpl.Execute(&buf, vars); err != nil {
		return nil, fmt.Errorf("failed to execute template: %w", err)
	}

	// text/template writes "<no value>" for a key the variables do not have,
	// and only an expression that goes through `default` is spared. Left
	// alone, that string lands in a manifest field — "cloud:<no value>" as an
	// image source, say — and the failure surfaces much later as a download
	// that cannot resolve.
	// Detected on the output rather than during execution: "missingkey=error"
	// would name the key directly, but it also fires on
	// "{{ .var | default `x` }}" — the whole point of which is that .var may be
	// absent — so every defaulted expression in every manifest would break.
	if i := bytes.Index(buf.Bytes(), marker); i >= 0 {
		// The line number, not the line. A rendered line can hold a value from
		// {{ secret ... }}, and this error travels into logs and API responses.
		return nil, fmt.Errorf("manifest line %d uses a variable that was not provided",
			bytes.Count(buf.Bytes()[:i], []byte("\n"))+1)
	}

	return bytes.ReplaceAll(buf.Bytes(), literalMarker, marker), nil
}

// templateFuncs returns the map of custom template functions
func templateFuncs(vars map[string]any, secrets SecretStore, scope string) template.FuncMap {
	fm := template.FuncMap{
		"default": func(args ...any) (any, error) {
			// In a pipe: {{ .var | default "fallback" }}
			// args[0] is the fallback, args[1] is the value from the pipe
			if len(args) < 1 {
				return nil, fmt.Errorf("default function requires at least one argument")
			}

			if len(args) == 1 {
				return args[0], nil
			}

			fallback := args[0]
			value := args[1]

			// Go template passes nil for missing map keys
			if value == nil {
				return fallback, nil
			}

			// Check for zero values of various types
			switch v := value.(type) {
			case string:
				if v == "" || v == "<no value>" {
					return fallback, nil
				}
			case int:
				if v == 0 {
					return fallback, nil
				}
			case int64:
				if v == 0 {
					return fallback, nil
				}
			case float64:
				if v == 0 {
					return fallback, nil
				}
			case bool:
				// bool is tricky, but usually default is used for strings/numbers
				return value, nil
			}

			return value, nil
		},
		"env": func(name string) string {
			// SECURITY: Only expose a whitelist of safe environment variables
			// to prevent information disclosure of secrets, tokens, or system paths.
			switch name {
			case "HOME", "USER", "LANG", "LC_ALL", "TZ", "PWD",
				"HOSPITUS_DATA_DIR", "HOSPITUS_STATE_DIR", "HOSPITUS_DB_PATH":
				return os.Getenv(name)
			default:
				return ""
			}
		},
		"randHex": func(n int) (string, error) {
			return generateRandomString(n, "hex")
		},
		"randAlnum": func(n int) (string, error) {
			return generateRandomString(n, "alnum")
		},
	}

	// Registered whether or not there is a store behind it. Without the
	// registration a manifest using {{ secret "db_pass" }} failed at parse
	// time with `function "secret" not defined`, which says nothing about the
	// missing store — and the same document parses fine as soon as one is
	// configured.
	fm["secret"] = func(name string) (string, error) {
		if secrets == nil {
			return "", fmt.Errorf("this manifest uses a secret (%q) but no secret store is configured", name)
		}
		if scope == "" {
			return "", fmt.Errorf("secret scope required but not provided")
		}
		return secrets.Get(scope, name)
	}

	return fm
}

// generateRandomString generates a random string of length n. It bounds n to a
// sane range and propagates any failure of the crypto RNG instead of silently
// emitting a predictable all-zero string.
func generateRandomString(n int, kind string) (string, error) {
	if n <= 0 || n > 256 {
		return "", fmt.Errorf("random length must be between 1 and 256, got %d", n)
	}

	switch kind {
	case "alnum":
		// Rejection sampling, not a modulo: 256 is 4*62 + 8, so mapping a raw
		// byte with "%" drew the first eight characters of the alphabet five
		// times out of 256 and the other fifty-four four times — 25% likelier,
		// in a value used as a generated password.
		const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
		const limit = 256 - (256 % len(charset)) // 248: bytes at or above it are redrawn
		result := make([]byte, n)
		buf := make([]byte, n)
		for filled := 0; filled < n; {
			if _, err := rand.Read(buf); err != nil {
				return "", fmt.Errorf("failed to read random bytes: %w", err)
			}
			for _, v := range buf {
				if int(v) >= limit {
					continue
				}
				result[filled] = charset[int(v)%len(charset)]
				filled++
				if filled == n {
					break
				}
			}
		}
		return string(result), nil
	default: // "hex"
		b := make([]byte, (n+1)/2)
		if _, err := rand.Read(b); err != nil {
			return "", fmt.Errorf("failed to read random bytes: %w", err)
		}
		return hex.EncodeToString(b)[:n], nil
	}
}

// marker is what text/template writes for a missing map key.
//
// literalMarker stands in for one the manifest wrote itself, for the length of
// the render. It contains a NUL, which no TOML document may hold, so it cannot
// collide with real content.
var (
	marker        = []byte("<no value>")
	literalMarker = []byte("\x00hospitus-literal-no-value\x00")
)

// hideLiteralMarkers replaces "<no value>" in a manifest's own text with a
// sentinel, leaving template actions untouched.
//
// Only outside {{ ... }}: a blanket ReplaceAll also rewrote the marker inside
// an action — "{{ if eq .mode `<no value>` }}" would then compare against the
// sentinel — which changes what the template does rather than what it renders.
func hideLiteralMarkers(raw []byte) []byte {
	var out bytes.Buffer
	out.Grow(len(raw))

	for i := 0; i < len(raw); {
		start := bytes.Index(raw[i:], []byte("{{"))
		if start < 0 {
			out.Write(bytes.ReplaceAll(raw[i:], marker, literalMarker))
			break
		}
		out.Write(bytes.ReplaceAll(raw[i:i+start], marker, literalMarker))
		i += start

		// The action, copied verbatim. An unterminated one is the parser's
		// problem to report, not this function's.
		//
		// The closing "}}" is found textually, so an action carrying that
		// sequence inside a string literal — {{ if eq .x "}}" }} — ends here
		// early, and the rest of it is treated as ordinary text. The only
		// consequence is that a literal "<no value>" later in that same action
		// would be swapped for the sentinel; nothing else changes, and the
		// template still parses and executes as written. Lexing an action
		// properly to close that would be more machinery than the case
		// warrants: no manifest has a reason to write "}}" in a string.
		end := bytes.Index(raw[i:], []byte("}}"))
		if end < 0 {
			out.Write(raw[i:])
			break
		}
		out.Write(raw[i : i+end+2])
		i += end + 2
	}

	return out.Bytes()
}
