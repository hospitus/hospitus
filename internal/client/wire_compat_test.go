package client_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/hospitus/hospitus/internal/api"
	"github.com/hospitus/hospitus/internal/client"
)

// TestClientTypesMatchTheDaemon compares the structs the client decodes into
// against the ones the daemon encodes.
//
// The two packages declare these types separately and nothing tied them
// together, so they drifted: VolumeInfo.Quota was an int64 of bytes on the
// daemon and a string on the client, which made every volume response fail with
// "cannot unmarshal number into Go struct field VolumeInfo.quota of type
// string" — volume list, create and info were all unusable. The same struct
// also carried a "size" field the daemon never sends, so the SIZE column always
// read 0 B.
func TestClientTypesMatchTheDaemon(t *testing.T) {
	pairs := []struct {
		name   string
		client interface{}
		daemon interface{}
	}{
		{"VolumeInfo", client.VolumeInfo{}, api.VolumeInfo{}},
		{"CreateVolumeRequest", client.CreateVolumeRequest{}, api.CreateVolumeRequest{}},
		{"NetworkInterfaceInfo", client.NetworkInterfaceInfo{}, api.NetworkInterfaceInfo{}},
		{"NetworkInterfaceRequest", client.NetworkInterfaceRequest{}, api.NetworkInterfaceRequest{}},
		{"ExposePortRequest", client.ExposePortRequest{}, api.ExposePortRequest{}},
		{"UnexposePortRequest", client.UnexposePortRequest{}, api.UnexposePortRequest{}},
		{"ServiceActionResult", client.ServiceActionResult{}, api.ServiceActionResult{}},
	}

	for _, p := range pairs {
		t.Run(p.name, func(t *testing.T) {
			daemon := jsonShape(reflect.TypeOf(p.daemon))
			for field, clientKind := range jsonShape(reflect.TypeOf(p.client)) {
				daemonKind, sent := daemon[field]
				if !sent {
					t.Errorf("the client declares %q, which the daemon never sends", field)
					continue
				}
				if clientKind != daemonKind {
					t.Errorf("%q decodes as %s on the client and is encoded as %s by the daemon",
						field, clientKind, daemonKind)
				}
			}
		})
	}
}

// jsonShape maps each JSON field name to the class of value it carries. Widths
// do not matter to encoding/json — int and int64 interchange freely — but a
// number and a string do not.
func jsonShape(t reflect.Type) map[string]string {
	shape := map[string]string{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" || tag == "" {
			continue
		}
		name := strings.Split(tag, ",")[0]
		if name == "" {
			continue
		}
		shape[name] = valueClass(f.Type)
	}
	return shape
}

func valueClass(t reflect.Type) string {
	switch t.Kind() {
	case reflect.String:
		return "a string"
	case reflect.Bool:
		return "a boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return "a number"
	case reflect.Slice, reflect.Array:
		return "an array of " + valueClass(t.Elem())
	case reflect.Map:
		return "an object"
	case reflect.Pointer:
		return valueClass(t.Elem())
	default:
		return t.String()
	}
}
