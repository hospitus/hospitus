package jail

import (
	"testing"
)

func TestValidateTmuxSessionName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{name: "valid simple", input: "mysession", wantErr: false},
		{name: "valid with dash", input: "my-session", wantErr: false},
		{name: "valid with underscore", input: "my_session", wantErr: false},
		{name: "valid with dot", input: "my.session", wantErr: false},
		{name: "valid numeric start", input: "1session", wantErr: false},
		{name: "valid single char", input: "a", wantErr: false},
		{name: "valid single digit", input: "0", wantErr: false},
		{name: "valid mixed", input: "web01-prod.v2_backup", wantErr: false},
		{name: "empty", input: "", wantErr: true},
		{name: "starts with dash", input: "-session", wantErr: true},
		{name: "starts with dot", input: ".session", wantErr: true},
		{name: "starts with underscore", input: "_session", wantErr: true},
		{name: "contains space", input: "my session", wantErr: true},
		{name: "contains semicolon", input: "my;session", wantErr: true},
		{name: "contains pipe", input: "my|session", wantErr: true},
		{name: "contains backtick", input: "my`session", wantErr: true},
		{name: "contains dollar", input: "my$session", wantErr: true},
		{name: "contains slash", input: "my/session", wantErr: true},
		{name: "max length 128 chars", input: "a" + string(make([]byte, 127)), wantErr: true}, // not alphanumeric fill
		{name: "exactly 128 chars valid", input: func() string {
			s := make([]byte, 128)
			for i := range s {
				s[i] = 'a'
			}
			return string(s)
		}(), wantErr: false},
		{name: "129 chars too long", input: func() string {
			s := make([]byte, 129)
			for i := range s {
				s[i] = 'a'
			}
			return string(s)
		}(), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateTmuxSessionName(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateTmuxSessionName(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

func TestValidTmuxSessionNameRegex(t *testing.T) {
	// Verify the regex pattern directly
	validCases := []string{"a", "Z", "0", "abc", "a-b", "a_b", "a.b", "A1-B2_C3.D4"}
	for _, c := range validCases {
		if !validTmuxSessionName.MatchString(c) {
			t.Errorf("Expected %q to match validTmuxSessionName regex", c)
		}
	}

	invalidCases := []string{"", "-a", ".a", "_a", " a", "a b", "a;b"}
	for _, c := range invalidCases {
		if validTmuxSessionName.MatchString(c) {
			t.Errorf("Expected %q to NOT match validTmuxSessionName regex", c)
		}
	}
}

func TestTmuxSessionStruct(t *testing.T) {
	session := TmuxSession{
		Name:      "web01",
		Windows:   3,
		Created:   "Mon Jun  1 12:00:00 2026",
		Attached:  true,
		SessionID: "$0",
	}

	if session.Name != "web01" {
		t.Errorf("Expected Name 'web01', got %q", session.Name)
	}
	if session.Windows != 3 {
		t.Errorf("Expected Windows 3, got %d", session.Windows)
	}
	if !session.Attached {
		t.Error("Expected Attached to be true")
	}
	if session.SessionID != "$0" {
		t.Errorf("Expected SessionID '$0', got %q", session.SessionID)
	}
}

func TestTmuxInfoStruct(t *testing.T) {
	info := TmuxInfo{
		Available: true,
		Sessions: []TmuxSession{
			{Name: "main", Windows: 1},
			{Name: "build", Windows: 2},
		},
		SuggestedCmd: "hospitus jail tmux myjail",
	}

	if !info.Available {
		t.Error("Expected Available to be true")
	}
	if len(info.Sessions) != 2 {
		t.Errorf("Expected 2 sessions, got %d", len(info.Sessions))
	}
	if info.Sessions[0].Name != "main" {
		t.Errorf("Expected first session name 'main', got %q", info.Sessions[0].Name)
	}
	if info.SuggestedCmd != "hospitus jail tmux myjail" {
		t.Errorf("Expected SuggestedCmd 'hospitus jail tmux myjail', got %q", info.SuggestedCmd)
	}
}

func TestTmuxInfoNotRunning(t *testing.T) {
	info := TmuxInfo{
		Available:    false,
		SuggestedCmd: "# Jail myjail is not running",
	}

	if info.Available {
		t.Error("Expected Available to be false for not running jail")
	}
	if len(info.Sessions) != 0 {
		t.Errorf("Expected 0 sessions, got %d", len(info.Sessions))
	}
}

func TestTmuxProviderInterfaceAssertion(t *testing.T) {
	// Verify compile-time assertion exists
	var _ TmuxProvider = (*JailProvider)(nil)
}
