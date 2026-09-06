package jail

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestGetVolumesParent(t *testing.T) {
	tests := []struct {
		name      string
		zfsParent string
		expected  string
	}{
		{
			name:      "standard path",
			zfsParent: "zroot/hospitus/jails",
			expected:  "zroot/hospitus/volumes",
		},
		{
			name:      "custom pool",
			zfsParent: "tank/virt/jails",
			expected:  "tank/virt/volumes",
		},
		{
			name:      "short path",
			zfsParent: "zroot",
			expected:  "zroot/hospitus/volumes",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &JailProvider{zfsParent: tt.zfsParent}
			result := p.getVolumesParent()
			if result != tt.expected {
				t.Errorf("getVolumesParent() = %s, expected %s", result, tt.expected)
			}
		})
	}
}

func TestVolumeStruct(t *testing.T) {
	volume := Volume{
		Name:        "mydata",
		Description: "My data volume",
		ZFSDataset:  "zroot/hospitus/volumes/mydata",
		Mountpoint:  "/zroot/hospitus/volumes/mydata",
		Quota:       10737418240, // 10GB
		Compression: "lz4",
		Labels: map[string]string{
			"app": "postgres",
		},
	}

	if volume.Name != "mydata" {
		t.Error("Volume Name mismatch")
	}
	if volume.Quota != 10737418240 {
		t.Error("Volume Quota mismatch")
	}
	if volume.Labels["app"] != "postgres" {
		t.Error("Volume Labels mismatch")
	}
}

func TestVolumeMountInfo(t *testing.T) {
	info := VolumeMountInfo{
		JailName:  "web",
		MountPath: "/data",
		ReadOnly:  false,
	}

	if info.JailName != "web" {
		t.Error("VolumeMountInfo JailName mismatch")
	}
	if info.MountPath != "/data" {
		t.Error("VolumeMountInfo MountPath mismatch")
	}
	if info.ReadOnly {
		t.Error("VolumeMountInfo ReadOnly should be false")
	}
}

func TestVolumeMount(t *testing.T) {
	mount := VolumeMount{
		VolumeName: "shared",
		MountPath:  "/var/shared",
		ReadOnly:   true,
	}

	if mount.VolumeName != "shared" {
		t.Error("VolumeMount VolumeName mismatch")
	}
	if mount.MountPath != "/var/shared" {
		t.Error("VolumeMount MountPath mismatch")
	}
	if !mount.ReadOnly {
		t.Error("VolumeMount ReadOnly should be true")
	}
}

func TestVolumeCreateOptions(t *testing.T) {
	opts := VolumeCreateOptions{
		Description: "Test volume",
		Quota:       "10G",
		Reservation: "1G",
		Compression: "zstd",
		Labels: map[string]string{
			"env": "prod",
		},
	}

	if opts.Description != "Test volume" {
		t.Error("VolumeCreateOptions Description mismatch")
	}
	if opts.Quota != "10G" {
		t.Error("VolumeCreateOptions Quota mismatch")
	}
	if opts.Compression != "zstd" {
		t.Error("VolumeCreateOptions Compression mismatch")
	}
}

func TestParseSize(t *testing.T) {
	tests := []struct {
		input    string
		expected int64
	}{
		{"", 0},
		{"0", 0},
		{"none", 0},
		{"NONE", 0},
		{"1K", 1024},
		{"10K", 10240},
		{"1M", 1048576},
		{"100M", 104857600},
		{"1G", 1073741824},
		{"10G", 10737418240},
		{"1T", 1099511627776},
		{"512m", 536870912},      // lowercase
		{"2g", 2147483648},       // lowercase
		{"  10G  ", 10737418240}, // with spaces
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := parseSize(tt.input)
			if result != tt.expected {
				t.Errorf("parseSize(%q) = %d, expected %d", tt.input, result, tt.expected)
			}
		})
	}
}

func TestSaveAndLoadVolumeMetadata(t *testing.T) {
	// Create temp state directory
	tmpDir, err := os.MkdirTemp("", "hospitus-volume-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	p := &JailProvider{stateDir: tmpDir}

	volume := &Volume{
		Name:        "testvolume",
		Description: "Test volume for unit tests",
		ZFSDataset:  "zroot/hospitus/volumes/testvolume",
		Mountpoint:  "/zroot/hospitus/volumes/testvolume",
		Quota:       5368709120, // 5GB
		Compression: "lz4",
		Labels: map[string]string{
			"app":     "test",
			"version": "1.0",
		},
		MountedTo: []VolumeMountInfo{
			{JailName: "web", MountPath: "/data", ReadOnly: false},
		},
	}

	err = p.saveVolumeMetadata(volume)
	if err != nil {
		t.Fatalf("saveVolumeMetadata failed: %v", err)
	}

	// Verify file was created
	metadataPath := filepath.Join(tmpDir, "volumes", "testvolume.json")
	if _, err := os.Stat(metadataPath); os.IsNotExist(err) {
		t.Error("Volume metadata file was not created")
	}

	loaded, err := p.loadVolumeMetadata("testvolume")
	if err != nil {
		t.Fatalf("loadVolumeMetadata failed: %v", err)
	}

	// Verify loaded data
	if loaded.Name != volume.Name {
		t.Errorf("Loaded Name = %s, expected %s", loaded.Name, volume.Name)
	}
	if loaded.Description != volume.Description {
		t.Errorf("Loaded Description = %s, expected %s", loaded.Description, volume.Description)
	}
	if loaded.Quota != volume.Quota {
		t.Errorf("Loaded Quota = %d, expected %d", loaded.Quota, volume.Quota)
	}
	if loaded.Labels["app"] != "test" {
		t.Error("Loaded Labels mismatch")
	}
	if len(loaded.MountedTo) != 1 {
		t.Error("Loaded MountedTo length mismatch")
	}
	if loaded.MountedTo[0].JailName != "web" {
		t.Error("Loaded MountedTo JailName mismatch")
	}
}

func TestAddToJailFstab(t *testing.T) {
	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "hospitus-fstab-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	p := &JailProvider{}
	fstabPath := filepath.Join(tmpDir, "fstab", "testjail")

	// Add first entry
	err = p.addToJailFstab(fstabPath, "/zroot/hospitus/volumes/data", "/data", false)
	if err != nil {
		t.Fatalf("addToJailFstab failed: %v", err)
	}

	// Add second entry (read-only)
	err = p.addToJailFstab(fstabPath, "/zroot/hospitus/volumes/shared", "/shared", true)
	if err != nil {
		t.Fatalf("addToJailFstab (second) failed: %v", err)
	}

	// Read and verify
	content, err := os.ReadFile(fstabPath)
	if err != nil {
		t.Fatalf("Failed to read fstab: %v", err)
	}

	contentStr := string(content)
	if !contains(contentStr, "/zroot/hospitus/volumes/data\t/data\tnullfs\trw") {
		t.Error("First fstab entry not found or incorrect")
	}
	if !contains(contentStr, "/zroot/hospitus/volumes/shared\t/shared\tnullfs\tro") {
		t.Error("Second fstab entry not found or incorrect")
	}
}

func TestRemoveFromJailFstab(t *testing.T) {
	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "hospitus-fstab-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	p := &JailProvider{}
	fstabPath := filepath.Join(tmpDir, "testjail")

	// Create fstab with entries
	content := `/zroot/hospitus/volumes/data	/data	nullfs	rw	0	0
/zroot/hospitus/volumes/shared	/shared	nullfs	ro	0	0
/zroot/hospitus/volumes/logs	/logs	nullfs	rw	0	0
`
	if err := os.WriteFile(fstabPath, []byte(content), 0o644); err != nil {
		t.Fatalf("Failed to write fstab: %v", err)
	}

	// Remove middle entry
	err = p.removeFromJailFstab(fstabPath, "/zroot/hospitus/volumes/shared")
	if err != nil {
		t.Fatalf("removeFromJailFstab failed: %v", err)
	}

	// Read and verify
	newContent, err := os.ReadFile(fstabPath)
	if err != nil {
		t.Fatalf("Failed to read fstab: %v", err)
	}

	contentStr := string(newContent)
	if !contains(contentStr, "/zroot/hospitus/volumes/data") {
		t.Error("First entry should still exist")
	}
	if contains(contentStr, "/zroot/hospitus/volumes/shared") {
		t.Error("Second entry should have been removed")
	}
	if !contains(contentStr, "/zroot/hospitus/volumes/logs") {
		t.Error("Third entry should still exist")
	}
}

func TestRemoveFromJailFstabNonExistent(t *testing.T) {
	p := &JailProvider{}

	// Should not error on non-existent file
	err := p.removeFromJailFstab("/nonexistent/path/fstab", "/some/source")
	if err != nil {
		t.Errorf("removeFromJailFstab should not error on non-existent file: %v", err)
	}
}

func TestCreateVolumeValidation(t *testing.T) {
	p := &JailProvider{
		zfsParent: "zroot/hospitus/jails",
		stateDir:  "/tmp/hospitus-test",
	}
	ctx := context.Background()

	// Test empty name
	_, err := p.CreateVolume(ctx, "", VolumeCreateOptions{})
	if err == nil {
		t.Error("CreateVolume should fail with empty name")
	}

	// Test name with slash
	_, err = p.CreateVolume(ctx, "bad/name", VolumeCreateOptions{})
	if err == nil {
		t.Error("CreateVolume should fail with slash in name")
	}

	// Test name with whitespace
	_, err = p.CreateVolume(ctx, "bad name", VolumeCreateOptions{})
	if err == nil {
		t.Error("CreateVolume should fail with whitespace in name")
	}

	_, err = p.CreateVolume(ctx, "bad\tname", VolumeCreateOptions{})
	if err == nil {
		t.Error("CreateVolume should fail with tab in name")
	}
}

func TestVolumeWithMounts(t *testing.T) {
	volume := Volume{
		Name:       "shared-data",
		ZFSDataset: "zroot/hospitus/volumes/shared-data",
		MountedTo: []VolumeMountInfo{
			{JailName: "web1", MountPath: "/data", ReadOnly: false},
			{JailName: "web2", MountPath: "/data", ReadOnly: true},
			{JailName: "backup", MountPath: "/backup/shared", ReadOnly: true},
		},
	}

	if len(volume.MountedTo) != 3 {
		t.Errorf("Expected 3 mounts, got %d", len(volume.MountedTo))
	}

	// Verify different mount configurations
	for _, mount := range volume.MountedTo {
		if mount.JailName == "" {
			t.Error("Mount should have JailName")
		}
		if mount.MountPath == "" {
			t.Error("Mount should have MountPath")
		}
	}

	// Verify web1 is read-write
	if volume.MountedTo[0].ReadOnly {
		t.Error("web1 mount should be read-write")
	}

	// Verify web2 is read-only
	if !volume.MountedTo[1].ReadOnly {
		t.Error("web2 mount should be read-only")
	}
}

func TestVolumeLabels(t *testing.T) {
	volume := Volume{
		Name: "labeled-vol",
		Labels: map[string]string{
			"environment": "production",
			"team":        "backend",
			"app":         "postgres",
			"version":     "15",
		},
	}

	if len(volume.Labels) != 4 {
		t.Errorf("Expected 4 labels, got %d", len(volume.Labels))
	}

	if volume.Labels["environment"] != "production" {
		t.Error("Environment label mismatch")
	}
	if volume.Labels["team"] != "backend" {
		t.Error("Team label mismatch")
	}
}

// Helper function
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// TestJailMountTargetRefusesASymlinkEscape covers a privilege escalation that
// starts inside the jail: its own root replaces a mount point with a symlink
// pointing back out, and the lexical check this replaced was satisfied.
func TestJailMountTargetRefusesASymlinkEscape(t *testing.T) {
	jailRoot := t.TempDir()
	outside := t.TempDir()

	if err := os.Symlink(outside, filepath.Join(jailRoot, "data")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if _, err := jailMountTarget(jailRoot, "/data"); err == nil {
		t.Error("a mount point symlinked out of the jail was accepted")
	}

	// A component in the middle of the path is the same escape.
	nested := filepath.Join(jailRoot, "srv")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(nested, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := jailMountTarget(jailRoot, "/srv/link/sub"); err == nil {
		t.Error("a mount point reached through a symlinked directory was accepted")
	}
}

// TestJailMountTargetAcceptsAnOrdinaryPath keeps the common case working: the
// mount point usually does not exist yet and is created by the caller.
func TestJailMountTargetAcceptsAnOrdinaryPath(t *testing.T) {
	jailRoot := t.TempDir()

	target, err := jailMountTarget(jailRoot, "/var/db/postgres")
	if err != nil {
		t.Fatalf("an ordinary mount point was refused: %v", err)
	}
	if want := filepath.Join(jailRoot, "var/db/postgres"); target != want {
		t.Errorf("target = %q, want %q", target, want)
	}

	if _, err := jailMountTarget(jailRoot, "/../etc"); err == nil {
		t.Error("a traversal out of the jail was accepted")
	}
}
