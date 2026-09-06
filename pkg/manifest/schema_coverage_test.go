package manifest

import (
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// waivedFields are schema fields no example manifest sets, each with the reason
// it cannot be one. A field belongs here only when an example would be worse
// for including it; anything else should be shown in a manifest instead.
var waivedFields = map[string]string{
	// Ships a cloud-config file verbatim and replaces every other cloud_init
	// setting, so an example that sets it demonstrates nothing else.
	// Covered by user_data_file_test.go.
	"cloud_init.user_data_file":           "mutually exclusive with the inline cloud-init example",
	"instances.cloud_init.user_data_file": "mutually exclusive with the inline cloud-init example",

	// Same value under a second name: the manifest carries one API version at
	// the top, and the stack form nests instances that inherit it.
	"instances.image.registry": "no example pulls from a private registry",
	"image.registry":           "no example pulls from a private registry",
}

// TestSchemaFieldsAppearInAnExample keeps the examples honest about the schema.
//
// The manifest reference documents every field below; a field no example sets
// is a field no test exercises end to end, and it rots without anyone noticing.
// The waiver list above is the escape hatch, and it costs a written reason.
func TestSchemaFieldsAppearInAnExample(t *testing.T) {
	schema := map[string]bool{}
	for _, root := range []reflect.Type{
		reflect.TypeOf(WorkloadManifest{}),
		reflect.TypeOf(StackManifest{}),
	} {
		for _, p := range schemaPaths(root, "") {
			schema[canonical(p)] = false
		}
	}

	templates := findExampleTemplates(t)
	if len(templates) == 0 {
		t.Fatal("no example manifests found")
	}

	for _, path := range templates {
		for _, key := range definedKeys(t, path) {
			key = canonical(key)
			for p := range schema {
				if pathMatches(p, key) {
					schema[p] = true
				}
			}
		}
	}

	var missing []string
	for p, covered := range schema {
		if covered {
			continue
		}
		if _, waived := waivedFields[p]; waived {
			continue
		}
		missing = append(missing, p)
	}
	sort.Strings(missing)

	for _, p := range missing {
		t.Errorf("no example manifest sets %q — add it to one, or waive it in waivedFields with a reason", p)
	}
	if len(missing) > 0 {
		t.Logf("%d of %d schema fields are unused by the examples", len(missing), len(schema))
	}
}

// canonical folds the stack form onto the workload form. A stack nests the
// same instance schema under [[instances]], so instances.networks.mtu and
// networks.mtu are one field, and an example only has to show it once.
func canonical(path string) string {
	return strings.TrimPrefix(path, "instances.")
}

// schemaPaths lists the dotted TOML path of every leaf field under t. A map
// with struct values contributes a "*" segment, because the file spells that
// segment as a name the schema does not fix (provider_overrides.qemu.cpu).
func schemaPaths(t reflect.Type, prefix string) []string {
	var out []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" {
			continue // unexported
		}

		name := strings.Split(f.Tag.Get("toml"), ",")[0]
		switch name {
		case "-":
			continue
		case "":
			name = strings.ToLower(f.Name)
		}
		path := name
		if prefix != "" {
			path = prefix + "." + name
		}

		ft := f.Type
		for ft.Kind() == reflect.Pointer || ft.Kind() == reflect.Slice {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Map {
			ft = ft.Elem()
			for ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			if ft.Kind() != reflect.Struct {
				out = append(out, path) // map of scalars: the map is the leaf
				continue
			}
			path += ".*"
		}

		if ft.Kind() == reflect.Struct && ft.PkgPath() == reflect.TypeOf(WorkloadManifest{}).PkgPath() {
			out = append(out, schemaPaths(ft, path)...)
			continue
		}
		out = append(out, path)
	}
	return out
}

// pathMatches reports whether a key from a file fills a schema path, treating
// "*" as any one segment.
func pathMatches(schemaPath, key string) bool {
	want := strings.Split(schemaPath, ".")
	got := strings.Split(key, ".")
	if len(want) != len(got) {
		return false
	}
	for i := range want {
		if want[i] != "*" && !strings.EqualFold(want[i], got[i]) {
			return false
		}
	}
	return true
}

// definedKeys renders an example and returns every key it actually sets.
func definedKeys(t *testing.T, templatePath string) []string {
	t.Helper()

	data, err := os.ReadFile(templatePath)
	if err != nil {
		t.Fatalf("read %s: %v", templatePath, err)
	}
	rendered, err := RenderTemplate(data, map[string]any{"name": "coverage"}, &mockSecretStore{}, "workload")
	if err != nil {
		t.Fatalf("render %s: %v", templatePath, err)
	}

	var target any
	if strings.Contains(string(rendered), "[stack]") {
		target = &StackManifest{}
	} else {
		target = &WorkloadManifest{}
	}
	md, err := toml.Decode(string(rendered), target)
	if err != nil {
		t.Fatalf("decode %s: %v", templatePath, err)
	}

	keys := make([]string, 0, len(md.Keys()))
	for _, k := range md.Keys() {
		keys = append(keys, k.String())
	}
	return keys
}
