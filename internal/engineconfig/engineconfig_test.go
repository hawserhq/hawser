package engineconfig

import (
	"reflect"
	"testing"
)

func TestParseValue(t *testing.T) {
	mustKey := func(name string) Key {
		k, ok := keyByName(name)
		if !ok {
			t.Fatalf("no such key %q", name)
		}
		return k
	}

	cases := []struct {
		key       string
		raw       string
		want      any
		wantClear bool
		wantErr   bool
	}{
		{"registry-mirrors", "https://a, https://b", []string{"https://a", "https://b"}, false, false},
		{"registry-mirrors", "", nil, true, false},
		{"log-driver", "local", "local", false, false},
		{"log-opts", "max-size=10m, max-file=3", map[string]string{"max-size": "10m", "max-file": "3"}, false, false},
		{"log-opts", "bogus", nil, false, true},
		{"max-concurrent-downloads", "6", 6, false, false},
		{"max-concurrent-downloads", "-1", nil, false, true},
		{"max-concurrent-downloads", "x", nil, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.key+"/"+tc.raw, func(t *testing.T) {
			got, clear, err := parseValue(mustKey(tc.key), tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if clear != tc.wantClear {
				t.Fatalf("clear = %v, want %v", clear, tc.wantClear)
			}
			if !clear && !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("value = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestRenderValueRoundTripsFromJSON(t *testing.T) {
	// Values as they decode from JSON: arrays are []any, numbers float64.
	cases := []struct {
		in   any
		want string
	}{
		{nil, ""},
		{"local", "local"},
		{[]any{"https://a", "https://b"}, "https://a,https://b"},
		{map[string]any{"max-file": "3", "max-size": "10m"}, "max-file=3,max-size=10m"},
		{float64(6), "6"},
		{true, "true"},
	}
	for _, tc := range cases {
		if got := renderValue(tc.in); got != tc.want {
			t.Errorf("renderValue(%#v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestUnknownKey(t *testing.T) {
	if _, ok := keyByName("no-such-key"); ok {
		t.Fatal("keyByName should reject unknown keys")
	}
}

func TestIsEngineKey(t *testing.T) {
	if !IsEngineKey("engine.dns") || IsEngineKey("idle-timeout") {
		t.Fatal("IsEngineKey misclassified")
	}
	if StripPrefix("engine.dns") != "dns" {
		t.Fatal("StripPrefix wrong")
	}
}
