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
		want    string
		invalid bool
	}{
		{name: "missing"}, {name: "null", present: true},
		{name: "priority", present: true, input: "priority", want: "priority"},
		{name: "fast alias", present: true, input: " Fast ", want: "priority"},
		{name: "auto", present: true, input: "auto", want: "auto"},
		{name: "default", present: true, input: "default", want: "default"},
		{name: "unknown", present: true, input: "private-tier", invalid: true},
		{name: "flex", present: true, input: "flex", invalid: true},
		{name: "object", present: true, input: object{"token": "must-not-echo"}, invalid: true},
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
			if text(wire["service_tier"]) != tc.want {
				t.Fatalf("tier=%v want %q", wire["service_tier"], tc.want)
			}
			if tc.want == "" {
				if _, ok := wire["service_tier"]; ok {
					t.Fatal("missing tier must remain absent")
				}
			}
			body, _, err = bridge.Reprepare(raw)
			if err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal(body, &wire); err != nil {
				t.Fatal(err)
			}
			if text(wire["service_tier"]) != tc.want {
				t.Fatal("Reprepare lost tier")
			}
		})
	}
}
