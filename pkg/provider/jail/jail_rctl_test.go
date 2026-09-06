package jail

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

// validateRCTLAmount applies the same validation rules as the inline code
// in SetResourceLimits, which is where the rule strings reach rctl(8).
func validateRCTLAmount(amount string) error {
	if !utf8.ValidString(amount) {
		return fmt.Errorf("invalid amount: must be valid UTF-8")
	}
	if strings.ContainsAny(amount, "\x00\r\n;&|$`(){}[]<>\\\"'") {
		return fmt.Errorf("invalid amount: contains invalid characters")
	}
	if len(amount) > 20 {
		return fmt.Errorf("invalid amount: too long (max 20 characters)")
	}
	return nil
}

func TestValidateRCTLAmount(t *testing.T) {
	tests := []struct {
		name    string
		amount  string
		wantErr bool
	}{
		{
			name:    "valid amount 100M",
			amount:  "100M",
			wantErr: false,
		},
		{
			name:    "valid amount 2G",
			amount:  "2G",
			wantErr: false,
		},
		{
			name:    "valid amount 50%",
			amount:  "50%",
			wantErr: false,
		},
		{
			name:    "valid amount numeric only",
			amount:  "3600",
			wantErr: false,
		},
		{
			name:    "amount with newline",
			amount:  "100M\n",
			wantErr: true,
		},
		{
			name:    "amount with semicolon",
			amount:  "100M;echo",
			wantErr: true,
		},
		{
			name:    "amount with pipe",
			amount:  "100M|cat",
			wantErr: true,
		},
		{
			name:    "amount with ampersand",
			amount:  "100M&",
			wantErr: true,
		},
		{
			name:    "amount with null byte",
			amount:  "100M\x00",
			wantErr: true,
		},
		{
			name:    "amount too long",
			amount:  "123456789012345678901",
			wantErr: true,
		},
		{
			name:    "amount exactly 20 chars ok",
			amount:  "12345678901234567890",
			wantErr: false,
		},
		{
			// Non-UTF8: invalid byte sequence
			name:    "non-utf8 amount",
			amount:  "100M\xfe\xfe",
			wantErr: true,
		},
		{
			name:    "amount with backtick",
			amount:  "100M`ls`",
			wantErr: true,
		},
		{
			name:    "amount with dollar sign",
			amount:  "$(whoami)",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateRCTLAmount(tt.amount)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateRCTLAmount(%q) error = %v, wantErr %v", tt.amount, err, tt.wantErr)
			}
		})
	}
}
