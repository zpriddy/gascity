package beads

import (
	"encoding/json"
	"testing"
)

// TestBead_Metadata_NonStringValues verifies that Bead.Metadata tolerates
// non-string JSON values (booleans, numbers, null) emitted by bd CLI's
// type-inferred metadata column. Without this tolerance, hook --claim
// crashes with "cannot unmarshal bool into Go struct field Bead.metadata
// of type string" on beads whose metadata contains values like true / 42.
//
// Source: sc-lmqvj (RCA: sc-iqq3 mechanik 2026-06-16).
func TestBead_Metadata_NonStringValues(t *testing.T) {
	cases := []struct {
		name string
		json string
		want map[string]string
	}{
		{
			name: "all strings",
			json: `{"id":"sc-1","metadata":{"k":"v"}}`,
			want: map[string]string{"k": "v"},
		},
		{
			name: "bool value",
			json: `{"id":"sc-2","metadata":{"flag":true}}`,
			want: map[string]string{"flag": "true"},
		},
		{
			name: "number value",
			json: `{"id":"sc-3","metadata":{"count":42}}`,
			want: map[string]string{"count": "42"},
		},
		{
			name: "null value",
			json: `{"id":"sc-4","metadata":{"empty":null}}`,
			want: map[string]string{"empty": ""},
		},
		{
			name: "mixed types",
			json: `{"id":"sc-5","metadata":{"a":"x","b":true,"c":7,"d":null}}`,
			want: map[string]string{"a": "x", "b": "true", "c": "7", "d": ""},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var b Bead
			if err := json.Unmarshal([]byte(tc.json), &b); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if len(b.Metadata) != len(tc.want) {
				t.Fatalf("Metadata len = %d, want %d (got %#v)", len(b.Metadata), len(tc.want), b.Metadata)
			}
			for k, want := range tc.want {
				got, ok := b.Metadata[k]
				if !ok {
					t.Errorf("Metadata[%q] missing", k)
					continue
				}
				if got != want {
					t.Errorf("Metadata[%q] = %q, want %q", k, got, want)
				}
			}
		})
	}
}
