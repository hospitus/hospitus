package jail

import "testing"

func TestFreeBSDArchPath(t *testing.T) {
	tests := map[string]string{
		"amd64":   "amd64",
		"i386":    "i386",
		"386":     "i386",
		"arm64":   "arm64/aarch64",
		"aarch64": "arm64/aarch64",
		"riscv64": "riscv/riscv64",
		"riscv":   "riscv/riscv64",
	}
	for arch, want := range tests {
		if got := freeBSDArchPath(arch); got != want {
			t.Errorf("freeBSDArchPath(%q) = %q, want %q", arch, got, want)
		}
	}
}

func TestValidatePackageName(t *testing.T) {
	valid := []string{"nginx", "www/nginx", "postgresql15-server", "nginx>=1.0", "py39-pip", "curl"}
	for _, name := range valid {
		if err := validatePackageName(name); err != nil {
			t.Errorf("validatePackageName(%q) rejected a valid name: %v", name, err)
		}
	}

	invalid := []string{"", "-rf", "nginx; rm", "pkg name", "a`b`", "$(x)", "foo|bar"}
	for _, name := range invalid {
		if err := validatePackageName(name); err == nil {
			t.Errorf("validatePackageName(%q) accepted an unsafe name", name)
		}
	}
}
