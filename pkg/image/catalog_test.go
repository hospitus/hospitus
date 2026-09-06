package image

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// --- helpers ---

func makeJailProfile(name string) ImageProfile {
	return ImageProfile{
		Name:      name,
		Display:   name,
		Category:  CategorySet,
		Format:    FormatTXZ,
		OSType:    "freebsd",
		OSVersion: "14.3-RELEASE",
		Arch:      "amd64",
		Providers: []ProviderType{ProviderJail},
		URL:       "https://example.com/" + name + ".txz",
	}
}

func makeVMProfile(name string) ImageProfile {
	return ImageProfile{
		Name:      name,
		Display:   name,
		Category:  CategoryCloud,
		Format:    FormatQCOW2,
		OSType:    "linux",
		OSVersion: "24.04",
		Arch:      "amd64",
		Providers: []ProviderType{ProviderQemu},
		URL:       "https://example.com/" + name + ".qcow2",
	}
}

// --- NewCatalog ---

func TestNewCatalog_BaseDirIsSet(t *testing.T) {
	dir := t.TempDir()
	c := NewCatalog(dir)
	if c.BaseDir() != dir {
		t.Errorf("BaseDir() = %q, want %q", c.BaseDir(), dir)
	}
}

func TestNewCatalog_LoadsBuiltinProfiles(t *testing.T) {
	dir := t.TempDir()
	c := NewCatalog(dir)
	all := c.Available()
	if len(all) == 0 {
		t.Error("Expected built-in profiles to be loaded from embedded catalog.json, got none")
	}
}

func TestNewCatalog_CacheMergesOverEmbedded(t *testing.T) {
	// Keep base under a temp root so the sibling "config" directory that
	// NewCatalog reads (filepath.Dir(base)/config) stays inside t.TempDir().
	root := t.TempDir()
	base := filepath.Join(root, "images")
	configDir := filepath.Join(root, "config")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cachePath := filepath.Join(configDir, "catalog.json")

	cacheProfiles := []ImageProfile{makeJailProfile("cache-only-profile")}
	data, _ := json.Marshal(cacheProfiles)
	if err := os.WriteFile(cachePath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	c := NewCatalog(base)

	// The cache entry must be merged in...
	if c.FindProfile("cache-only-profile") == nil {
		t.Error("expected cache-only-profile to be merged into the catalog")
	}
	// ...and the trusted embedded entries must NOT be dropped by the cache.
	if len(c.Available()) <= 1 {
		t.Errorf("expected embedded profiles to be preserved alongside the cache entry, got %d", len(c.Available()))
	}
}

// --- NewCatalogWithFile ---

func TestNewCatalogWithFile_LoadsFromFile(t *testing.T) {
	dir := t.TempDir()
	profiles := []ImageProfile{makeJailProfile("custom-file-profile")}
	data, _ := json.Marshal(profiles)
	catalogPath := filepath.Join(dir, "my-catalog.json")
	if err := os.WriteFile(catalogPath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	c := NewCatalogWithFile(dir, catalogPath)
	all := c.Available()
	if len(all) != 1 || all[0].Name != "custom-file-profile" {
		t.Errorf("Expected 1 profile from file, got %d", len(all))
	}
}

func TestNewCatalogWithFile_FallsBackToEmbedded(t *testing.T) {
	dir := t.TempDir()
	nonExistent := filepath.Join(dir, "does-not-exist.json")

	c := NewCatalogWithFile(dir, nonExistent)
	all := c.Available()
	if len(all) == 0 {
		t.Error("Expected fallback to embedded catalog, got no profiles")
	}
}

func TestNewCatalogWithFile_FallsBackOnInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	badPath := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(badPath, []byte("not json{{{"), 0o644); err != nil {
		t.Fatal(err)
	}

	c := NewCatalogWithFile(dir, badPath)
	all := c.Available()
	if len(all) == 0 {
		t.Error("Expected fallback to embedded catalog on bad JSON, got no profiles")
	}
}

// --- BaseDir and GetSubDir ---

func TestBaseDir(t *testing.T) {
	dir := t.TempDir()
	c := NewCatalog(dir)
	if got := c.BaseDir(); got != dir {
		t.Errorf("BaseDir() = %q, want %q", got, dir)
	}
}

func TestGetSubDir(t *testing.T) {
	dir := t.TempDir()
	c := NewCatalog(dir)

	tests := []struct {
		category ImageCategory
		want     string
	}{
		{CategorySet, filepath.Join(dir, "sets")},
		{CategoryISO, filepath.Join(dir, "iso")},
		{CategoryCloud, filepath.Join(dir, "cloud")},
		{"unknown", dir},
		{"", dir},
	}

	for _, tt := range tests {
		got := c.GetSubDir(tt.category)
		if got != tt.want {
			t.Errorf("GetSubDir(%q) = %q, want %q", tt.category, got, tt.want)
		}
	}
}

// --- AddBuiltinProfile ---

func TestAddBuiltinProfile_Adds(t *testing.T) {
	c := &Catalog{baseDir: t.TempDir()}
	p := makeJailProfile("test-add-profile")
	c.AddBuiltinProfile(p)

	found := c.FindProfile("test-add-profile")
	if found == nil {
		t.Fatal("Expected profile to be found after AddBuiltinProfile")
	}
	if found.Name != "test-add-profile" {
		t.Errorf("Name = %q, want %q", found.Name, "test-add-profile")
	}
}

func TestAddBuiltinProfile_UpdatesExisting(t *testing.T) {
	c := &Catalog{baseDir: t.TempDir()}
	p := makeJailProfile("update-me")
	c.AddBuiltinProfile(p)

	updated := p
	updated.Description = "updated description"
	c.AddBuiltinProfile(updated)

	all := c.Available()
	count := 0
	for _, a := range all {
		if a.Name == "update-me" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("Expected exactly 1 profile named 'update-me', got %d", count)
	}

	found := c.FindProfile("update-me")
	if found.Description != "updated description" {
		t.Errorf("Description not updated: got %q", found.Description)
	}
}

// --- Available ---

func TestAvailable_ReturnsBothBuiltinAndCustom(t *testing.T) {
	c := &Catalog{baseDir: t.TempDir()}
	c.AddBuiltinProfile(makeJailProfile("builtin-one"))
	c.AddCustomProfile(makeVMProfile("custom-one"))

	all := c.Available()
	names := map[string]bool{}
	for _, p := range all {
		names[p.Name] = true
	}

	if !names["builtin-one"] {
		t.Error("Expected builtin-one in Available()")
	}
	if !names["custom-one"] {
		t.Error("Expected custom-one in Available()")
	}
}

func TestAvailable_EmptyOnFreshCatalog(t *testing.T) {
	c := &Catalog{baseDir: t.TempDir()}
	all := c.Available()
	if len(all) != 0 {
		t.Errorf("Expected 0 profiles on fresh empty catalog, got %d", len(all))
	}
}

// --- AvailableByCategory ---

func TestAvailableByCategory_FiltersCorrectly(t *testing.T) {
	c := &Catalog{baseDir: t.TempDir()}
	c.AddBuiltinProfile(makeJailProfile("jail-set"))
	c.AddBuiltinProfile(makeVMProfile("cloud-img"))

	sets := c.AvailableByCategory(CategorySet)
	if len(sets) != 1 || sets[0].Name != "jail-set" {
		t.Errorf("AvailableByCategory(CategorySet) returned unexpected results: %+v", sets)
	}

	clouds := c.AvailableByCategory(CategoryCloud)
	if len(clouds) != 1 || clouds[0].Name != "cloud-img" {
		t.Errorf("AvailableByCategory(CategoryCloud) returned unexpected results: %+v", clouds)
	}
}

func TestAvailableByCategory_IncludesCustomProfiles(t *testing.T) {
	c := &Catalog{baseDir: t.TempDir()}
	custom := makeJailProfile("custom-set")
	c.AddCustomProfile(custom)

	sets := c.AvailableByCategory(CategorySet)
	if len(sets) != 1 || sets[0].Name != "custom-set" {
		t.Errorf("Expected custom profile in AvailableByCategory results: %+v", sets)
	}
}

func TestAvailableByCategory_NoneMatchReturnsNil(t *testing.T) {
	c := &Catalog{baseDir: t.TempDir()}
	c.AddBuiltinProfile(makeJailProfile("only-set"))

	isos := c.AvailableByCategory(CategoryISO)
	if len(isos) != 0 {
		t.Errorf("Expected 0 ISO profiles, got %d", len(isos))
	}
}

// --- AvailableByProvider ---

func TestAvailableByProvider_FiltersJail(t *testing.T) {
	c := &Catalog{baseDir: t.TempDir()}
	c.AddBuiltinProfile(makeJailProfile("jail-profile"))
	c.AddBuiltinProfile(makeVMProfile("qemu-profile"))

	jailImgs := c.AvailableByProvider(ProviderJail)
	if len(jailImgs) != 1 || jailImgs[0].Name != "jail-profile" {
		t.Errorf("AvailableByProvider(ProviderJail) = %+v", jailImgs)
	}
}

func TestAvailableByProvider_FiltersQemu(t *testing.T) {
	c := &Catalog{baseDir: t.TempDir()}
	c.AddBuiltinProfile(makeJailProfile("jail-profile"))
	c.AddBuiltinProfile(makeVMProfile("qemu-profile"))

	qemuImgs := c.AvailableByProvider(ProviderQemu)
	if len(qemuImgs) != 1 || qemuImgs[0].Name != "qemu-profile" {
		t.Errorf("AvailableByProvider(ProviderQemu) = %+v", qemuImgs)
	}
}

func TestAvailableByProvider_MultipleProviders(t *testing.T) {
	c := &Catalog{baseDir: t.TempDir()}
	multi := ImageProfile{
		Name:      "multi-provider",
		Category:  CategoryCloud,
		Format:    FormatQCOW2,
		Providers: []ProviderType{ProviderBhyve, ProviderQemu},
	}
	c.AddBuiltinProfile(multi)

	bhyveImgs := c.AvailableByProvider(ProviderBhyve)
	qemuImgs := c.AvailableByProvider(ProviderQemu)

	if len(bhyveImgs) != 1 {
		t.Errorf("Expected 1 bhyve image, got %d", len(bhyveImgs))
	}
	if len(qemuImgs) != 1 {
		t.Errorf("Expected 1 qemu image, got %d", len(qemuImgs))
	}
}

func TestAvailableByProvider_IncludesCustom(t *testing.T) {
	c := &Catalog{baseDir: t.TempDir()}
	c.AddCustomProfile(makeJailProfile("custom-jail"))

	jailImgs := c.AvailableByProvider(ProviderJail)
	if len(jailImgs) != 1 || jailImgs[0].Name != "custom-jail" {
		t.Errorf("Expected custom jail profile, got %+v", jailImgs)
	}
}

// --- FindProfile ---

func TestFindProfile_ByName(t *testing.T) {
	c := &Catalog{baseDir: t.TempDir()}
	c.AddBuiltinProfile(makeJailProfile("find-me"))

	found := c.FindProfile("find-me")
	if found == nil {
		t.Fatal("FindProfile returned nil for existing profile")
	}
	if found.Name != "find-me" {
		t.Errorf("Found wrong profile: %q", found.Name)
	}
}

func TestFindProfile_NotFound(t *testing.T) {
	c := &Catalog{baseDir: t.TempDir()}
	c.AddBuiltinProfile(makeJailProfile("exists"))

	found := c.FindProfile("does-not-exist")
	if found != nil {
		t.Errorf("Expected nil for missing profile, got %+v", found)
	}
}

func TestFindProfile_SearchesCustom(t *testing.T) {
	c := &Catalog{baseDir: t.TempDir()}
	c.AddCustomProfile(makeVMProfile("custom-vm"))

	found := c.FindProfile("custom-vm")
	if found == nil {
		t.Fatal("FindProfile did not find custom profile")
	}
}

func TestFindProfile_BuiltinTakesOrderPrecedence(t *testing.T) {
	c := &Catalog{baseDir: t.TempDir()}
	builtin := makeJailProfile("shared-name")
	builtin.Description = "builtin"
	custom := makeJailProfile("shared-name")
	custom.Description = "custom"

	c.AddBuiltinProfile(builtin)
	c.AddCustomProfile(custom)

	// FindProfile iterates builtin first
	found := c.FindProfile("shared-name")
	if found == nil {
		t.Fatal("FindProfile returned nil")
	}
	if found.Description != "builtin" {
		t.Errorf("Expected builtin profile to be returned first, got description=%q", found.Description)
	}
}

// --- getCategoryByExtension ---

func TestGetCategoryByExtension(t *testing.T) {
	tests := []struct {
		ext  string
		want ImageCategory
	}{
		{".txz", CategorySet},
		{".tar.xz", CategorySet},
		{".tar.gz", CategorySet},
		{".tgz", CategorySet},
		{".iso", CategoryISO},
		{".raw", CategoryCloud},
		{".qcow2", CategoryCloud},
		{".vmdk", CategoryCloud},
		{".img", CategoryCloud},
		{".unknown", ""},
		{"", ""},
		{".zip", ""},
		// Full file names, including multi-part suffixes that filepath.Ext
		// alone could not classify (it would yield ".xz"/".gz").
		{"base.txz", CategorySet},
		{"freebsd-14.tar.xz", CategorySet},
		{"userland.tar.gz", CategorySet},
		{"ubuntu.qcow2", CategoryCloud},
		{"installer.iso", CategoryISO},
		{"notes.txt", ""},
	}

	for _, tt := range tests {
		got := getCategoryByExtension(tt.ext)
		if got != tt.want {
			t.Errorf("getCategoryByExtension(%q) = %q, want %q", tt.ext, got, tt.want)
		}
	}
}

// --- SaveBuiltinCatalog ---

func TestSaveBuiltinCatalog_WritesValidJSON(t *testing.T) {
	dir := t.TempDir()
	c := &Catalog{baseDir: dir}
	c.AddBuiltinProfile(makeJailProfile("save-test"))

	outPath := filepath.Join(dir, "out-catalog.json")
	if err := c.SaveBuiltinCatalog(outPath); err != nil {
		t.Fatalf("SaveBuiltinCatalog() error: %v", err)
	}

	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("Failed to read saved catalog: %v", err)
	}

	var loaded []ImageProfile
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("Saved catalog is invalid JSON: %v", err)
	}

	if len(loaded) != 1 || loaded[0].Name != "save-test" {
		t.Errorf("Saved catalog has unexpected contents: %+v", loaded)
	}
}

func TestSaveBuiltinCatalog_ErrorOnBadPath(t *testing.T) {
	c := &Catalog{baseDir: t.TempDir()}
	c.AddBuiltinProfile(makeJailProfile("p"))

	err := c.SaveBuiltinCatalog("/nonexistent/dir/catalog.json")
	if err == nil {
		t.Error("Expected error when saving to nonexistent directory, got nil")
	}
}

// --- AddCustomProfile / SaveCustomProfiles / LoadCustomProfiles ---

func TestAddCustomProfile_Adds(t *testing.T) {
	c := &Catalog{baseDir: t.TempDir()}
	c.AddCustomProfile(makeVMProfile("custom-add"))

	found := c.FindProfile("custom-add")
	if found == nil {
		t.Fatal("Custom profile not found after AddCustomProfile")
	}
}

func TestAddCustomProfile_UpdatesExisting(t *testing.T) {
	c := &Catalog{baseDir: t.TempDir()}
	p := makeVMProfile("replace-me")
	c.AddCustomProfile(p)

	updated := p
	updated.Description = "replaced"
	c.AddCustomProfile(updated)

	all := c.Available()
	count := 0
	for _, a := range all {
		if a.Name == "replace-me" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("Expected 1 custom profile named 'replace-me', got %d", count)
	}
	found := c.FindProfile("replace-me")
	if found.Description != "replaced" {
		t.Errorf("Description not updated: %q", found.Description)
	}
}

func TestSaveAndLoadCustomProfiles_Roundtrip(t *testing.T) {
	dir := t.TempDir()
	c := &Catalog{baseDir: dir}
	c.AddCustomProfile(makeVMProfile("roundtrip-vm"))
	c.AddCustomProfile(makeJailProfile("roundtrip-jail"))

	savePath := filepath.Join(dir, "custom-profiles.json")
	if err := c.SaveCustomProfiles(savePath); err != nil {
		t.Fatalf("SaveCustomProfiles() error: %v", err)
	}

	c2 := &Catalog{baseDir: dir}
	if err := c2.LoadCustomProfiles(savePath); err != nil {
		t.Fatalf("LoadCustomProfiles() error: %v", err)
	}

	all := c2.Available()
	names := map[string]bool{}
	for _, p := range all {
		names[p.Name] = true
	}

	if !names["roundtrip-vm"] || !names["roundtrip-jail"] {
		t.Errorf("Profiles not restored after roundtrip: %+v", all)
	}
}

func TestLoadCustomProfiles_NonExistentFileIsNoOp(t *testing.T) {
	dir := t.TempDir()
	c := &Catalog{baseDir: dir}
	err := c.LoadCustomProfiles(filepath.Join(dir, "missing.json"))
	if err != nil {
		t.Errorf("LoadCustomProfiles on missing file should not error, got: %v", err)
	}
	if len(c.Available()) != 0 {
		t.Error("Expected no profiles after loading non-existent file")
	}
}

func TestLoadCustomProfiles_InvalidJSONReturnsError(t *testing.T) {
	dir := t.TempDir()
	badPath := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(badPath, []byte("{bad json"), 0o644); err != nil {
		t.Fatal(err)
	}

	c := &Catalog{baseDir: dir}
	if err := c.LoadCustomProfiles(badPath); err == nil {
		t.Error("Expected error on invalid JSON, got nil")
	}
}

func TestSaveCustomProfiles_EmptySlice(t *testing.T) {
	dir := t.TempDir()
	c := &Catalog{baseDir: dir}
	savePath := filepath.Join(dir, "empty.json")
	if err := c.SaveCustomProfiles(savePath); err != nil {
		t.Fatalf("SaveCustomProfiles on empty catalog: %v", err)
	}

	data, err := os.ReadFile(savePath)
	if err != nil {
		t.Fatalf("Failed to read saved file: %v", err)
	}
	var loaded []ImageProfile
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("Saved empty profiles is invalid JSON: %v", err)
	}
}

// --- ResolveProfile ---

// TestResolveProfileAcceptsBothSpellings is what keeps `hospitus image fetch` from
// downloading one release twice: the catalog name and the short name the guides
// teach have to land on the same entry, or the short one misses the catalog and
// is fetched separately into the images root.
func TestResolveProfileAcceptsBothSpellings(t *testing.T) {
	c := &Catalog{baseDir: t.TempDir()}
	c.AddBuiltinProfile(makeJailProfile("freebsd-14.3-RELEASE-amd64"))

	for _, name := range []string{"freebsd-14.3-RELEASE-amd64", "14.3-RELEASE-amd64"} {
		found := c.ResolveProfile(name)
		if found == nil {
			t.Fatalf("ResolveProfile(%q) found nothing", name)
		}
		if found.Name != "freebsd-14.3-RELEASE-amd64" {
			t.Errorf("ResolveProfile(%q) = %q, want the catalog name", name, found.Name)
		}
	}
}

func TestResolveProfileUnknown(t *testing.T) {
	c := &Catalog{baseDir: t.TempDir()}
	if found := c.ResolveProfile("no-such-image"); found != nil {
		t.Errorf("ResolveProfile invented a profile: %+v", found)
	}
}

// --- findProfileByFilename ---

func TestFindProfileByFilename_ExactMatch(t *testing.T) {
	c := &Catalog{baseDir: t.TempDir()}
	c.AddBuiltinProfile(makeJailProfile("freebsd-14.3-RELEASE-amd64"))

	found := c.findProfileByFilename("freebsd-14.3-RELEASE-amd64.txz")
	if found == nil {
		t.Fatal("findProfileByFilename did not find profile by filename")
	}
	if found.Name != "freebsd-14.3-RELEASE-amd64" {
		t.Errorf("Wrong profile: %q", found.Name)
	}
}

func TestFindProfileByFilename_FreeBSDPrefix(t *testing.T) {
	c := &Catalog{baseDir: t.TempDir()}
	c.AddBuiltinProfile(makeJailProfile("freebsd-14.3-RELEASE-amd64"))

	found := c.findProfileByFilename("14.3-RELEASE-amd64.txz")
	if found == nil {
		t.Fatal("findProfileByFilename did not match freebsd-prefixed profile")
	}
}

func TestFindProfileByFilename_NotFound(t *testing.T) {
	c := &Catalog{baseDir: t.TempDir()}
	found := c.findProfileByFilename("unknown-image.txz")
	if found != nil {
		t.Errorf("Expected nil, got %+v", found)
	}
}

// --- ListDownloaded ---

func TestListDownloaded_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	c := NewCatalog(dir)

	imgs, err := c.ListDownloaded()
	if err != nil {
		t.Fatalf("ListDownloaded() error: %v", err)
	}
	if len(imgs) != 0 {
		t.Errorf("Expected 0 images in empty dir, got %d", len(imgs))
	}
}

func TestListDownloaded_SkipsPartialFiles(t *testing.T) {
	dir := t.TempDir()
	c := NewCatalog(dir)

	if err := os.WriteFile(filepath.Join(dir, "image.txz.partial"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	imgs, err := c.ListDownloaded()
	if err != nil {
		t.Fatalf("ListDownloaded() error: %v", err)
	}
	if len(imgs) != 0 {
		t.Errorf("Expected partial files to be skipped, got %d items", len(imgs))
	}
}

func TestListDownloaded_FindsFilesInSubdirs(t *testing.T) {
	dir := t.TempDir()
	c := NewCatalog(dir)

	setsDir := filepath.Join(dir, "sets")
	if err := os.MkdirAll(setsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(setsDir, "base.txz"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	imgs, err := c.ListDownloaded()
	if err != nil {
		t.Fatalf("ListDownloaded() error: %v", err)
	}
	if len(imgs) != 1 {
		t.Errorf("Expected 1 image in sets subdir, got %d", len(imgs))
	}
	if imgs[0].Category != CategorySet {
		t.Errorf("Expected CategorySet, got %q", imgs[0].Category)
	}
}

func TestListDownloaded_SkipsUnknownExtensions(t *testing.T) {
	dir := t.TempDir()
	c := NewCatalog(dir)

	if err := os.WriteFile(filepath.Join(dir, "somefile.exe"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	imgs, err := c.ListDownloaded()
	if err != nil {
		t.Fatalf("ListDownloaded() error: %v", err)
	}
	if len(imgs) != 0 {
		t.Errorf("Expected unknown extension to be skipped, got %d items", len(imgs))
	}
}

func TestListDownloaded_MatchesProfiles(t *testing.T) {
	dir := t.TempDir()
	c := NewCatalog(dir)
	c.AddBuiltinProfile(makeJailProfile("freebsd-14.3-RELEASE-amd64"))

	setsDir := filepath.Join(dir, "sets")
	if err := os.MkdirAll(setsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(setsDir, "freebsd-14.3-RELEASE-amd64.txz"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	imgs, err := c.ListDownloaded()
	if err != nil {
		t.Fatalf("ListDownloaded() error: %v", err)
	}
	if len(imgs) != 1 {
		t.Fatalf("Expected 1 image, got %d", len(imgs))
	}
	if imgs[0].Profile == nil {
		t.Error("Expected profile to be matched for freebsd-14.3-RELEASE-amd64.txz")
	}
}

// --- freeBSDBaseURL ---

func TestFreeBSDBaseURL(t *testing.T) {
	cases := []struct {
		arch, version, want string
	}{
		{"amd64", "14.3-RELEASE", "https://download.freebsd.org/releases/amd64/14.3-RELEASE/base.txz"},
		{"i386", "14.3-RELEASE", "https://download.freebsd.org/releases/i386/14.3-RELEASE/base.txz"},
		{"arm64", "14.3-RELEASE", "https://download.freebsd.org/releases/arm64/aarch64/14.3-RELEASE/base.txz"},
		{"aarch64", "14.3-RELEASE", "https://download.freebsd.org/releases/arm64/aarch64/14.3-RELEASE/base.txz"},
		{"riscv64", "14.3-RELEASE", "https://download.freebsd.org/releases/riscv/riscv64/14.3-RELEASE/base.txz"},
		{"riscv", "14.3-RELEASE", "https://download.freebsd.org/releases/riscv/riscv64/14.3-RELEASE/base.txz"},
	}
	for _, c := range cases {
		if got := freeBSDBaseURL(c.arch, c.version); got != c.want {
			t.Errorf("freeBSDBaseURL(%q, %q) = %q, want %q", c.arch, c.version, got, c.want)
		}
	}
}
