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

	tmpl, err := template.New("manifest").
		Funcs(templateFuncs(vars, secrets, scope)).
		Option("missingkey=zero").
		Parse(string(raw))
	if err != nil {
		return nil, fmt.Errorf("failed to parse template: %w", err)
	}

	var buf bytes.Buffer
	// For map data, missing keys will return nil or zero value depending on options.
	// Our custom 'default' function handles these.
	if err := tmpl.Execute(&buf, vars); err != nil {
		return nil, fmt.Errorf("failed to execute template: %w", err)
	}

	return buf.Bytes(), nil
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

	if secrets != nil {
		fm["secret"] = func(name string) (string, error) {
			if scope == "" {
				return "", fmt.Errorf("secret scope required but not provided")
			}
			return secrets.Get(scope, name)
		}
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
		b := make([]byte, n)
		if _, err := rand.Read(b); err != nil {
			return "", fmt.Errorf("failed to read random bytes: %w", err)
		}
		const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
		result := make([]byte, n)
		for i := range result {
			result[i] = charset[int(b[i])%len(charset)]
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
