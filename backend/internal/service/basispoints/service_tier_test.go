package basispoints

import (
	"encoding/json"
	"testing"
)

func TestBPSServiceTierWireContract(t *testing.T) {
	for _, tc := range []struct {
		name    string
		input   any
		present bool
		invalid bool
	}{
		{name: "missing"}, {name: "null", present: true},
		{name: "priority", present: true, input: "priority"},
		{name: "fast alias", present: true, input: " Fast "},
		{name: "auto", present: true, input: "auto"},
		{name: "default", present: true, input: "default"},
		{name: "unknown", present: true, input: "private-tier", invalid: true},
		{name: "flex", present: true, input: "flex", invalid: true},
		{name: "object", present: true, input: object{"token": "must-not-echo"}, invalid: true},
		{name: "number", present: true, input: 1, invalid: true},
		{name: "boolean", present: true, input: true, invalid: true},
		{name: "array", present: true, input: []any{"priority"}, invalid: true},
		{name: "empty", present: true, input: "", invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := testSource()
			if tc.present {
				source["service_tier"] = tc.input
			}
			raw, _ := json.Marshal(source)
			body, bridge, err := Prepare(raw, "fixture", nil)
			if tc.invalid {
				if err == nil {
					t.Fatal("unsupported tier accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var wire object
			if err = json.Unmarshal(body, &wire); err != nil {
				t.Fatal(err)
			}
			if _, exists := wire["service_tier"]; exists {
				t.Fatal("compatibility tier must not reach the BPS endpoint")
			}
			body, _, err = bridge.Reprepare(raw)
			if err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal(body, &wire); err != nil {
				t.Fatal(err)
			}
			if _, exists := wire["service_tier"]; exists {
				t.Fatal("Reprepare must not reintroduce the compatibility tier")
			}
		})
	}
}
