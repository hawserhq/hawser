package apibody

import "testing"

// The bypass this package exists for, in one test: a document that spells a
// field two ways is judged as the harmless spelling and acted on as the other.
func TestAmbiguousCatchesCaseVariantDuplicates(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{"top-level image", `{"Image":"ok","image":"evil"}`, true},
		{"nested privileged", `{"HostConfig":{"Privileged":false,"privileged":true}}`, true},
		{"sibling host configs", `{"HostConfig":{"Privileged":false},"hostconfig":{"Privileged":true}}`, true},
		{"upper case", `{"Image":"ok","IMAGE":"evil"}`, true},
		{"inside an array (mounts)", `{"HostConfig":{"Mounts":[{"Source":"/a","source":"/"}]}}`, true},
		{"deep", `{"a":{"b":{"Binds":1,"binds":2}}}`, true},

		// Only GUARDED names are checked: a duplicate elsewhere cannot bypass
		// anything, and data-keyed objects legitimately differ only in case.
		{"unguarded name", `{"a":1,"A":2}`, false},
		{"labels differing in case", `{"Labels":{"app":"x","App":"y"}}`, false},

		{"ordinary body", `{"Image":"busybox","HostConfig":{"Privileged":false}}`, false},
		{"distinct fields", `{"Image":"x","Cmd":["a"],"Env":["A=1"]}`, false},
		{"empty", `{}`, false},
		{"scalar", `"hello"`, false},
		{"malformed is not our business", `{"a":`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			field, got := Ambiguous([]byte(c.raw))
			if got != c.want {
				t.Errorf("Ambiguous(%s) = %v (%q), want %v", c.raw, got, field, c.want)
			}
			if got && field == "" {
				t.Error("reported ambiguous without naming the field")
			}
		})
	}
}

// Data-keyed objects must not be mistaken for field-name collisions. Labels and
// Volumes are keyed by user data, where two spellings are legitimate and
// meaningful -- refusing them would break real containers.
func TestAmbiguousAllowsDataKeyedObjects(t *testing.T) {
	// Distinct label keys that merely differ in case are two different labels.
	if f, got := Ambiguous([]byte(`{"Labels":{"app":"x","App":"y"}}`)); got {
		t.Errorf("refused legitimate data keys: %q", f)
	}
}

func TestFieldFoldsCase(t *testing.T) {
	m := map[string]any{"image": "busybox"}
	if got := String(m, "Image"); got != "busybox" {
		t.Errorf(`String(m,"Image") = %q; a lowercase spelling must still be judged`, got)
	}
	m2 := map[string]any{"IMAGE": "busybox"}
	if got := String(m2, "Image"); got != "busybox" {
		t.Errorf(`String(m,"Image") on IMAGE = %q`, got)
	}
}

func TestFieldPrefersTheExactSpelling(t *testing.T) {
	// Ambiguous() refuses this upstream, but Field must still be deterministic.
	m := map[string]any{"Image": "exact", "image": "folded"}
	if got := String(m, "Image"); got != "exact" {
		t.Errorf("String = %q, want the exact key", got)
	}
}

func TestMapAndSliceFoldCase(t *testing.T) {
	m := map[string]any{
		"hostconfig": map[string]any{"binds": []any{"a:b"}},
	}
	hc, ok := Map(m, "HostConfig")
	if !ok {
		t.Fatal(`Map(m,"HostConfig") missed a lowercase spelling`)
	}
	if binds, ok := Slice(hc, "Binds"); !ok || len(binds) != 1 {
		t.Errorf(`Slice(hc,"Binds") = %v, %v`, binds, ok)
	}
}

// A translated value has to replace the spelling the daemon will read, not sit
// beside it -- otherwise the original survives and the translation is void.
func TestSetFieldWritesThroughToTheExistingSpelling(t *testing.T) {
	m := map[string]any{"binds": []any{"old"}}
	SetField(m, "Binds", []any{"new"})
	if len(m) != 1 {
		t.Fatalf("SetField added a second spelling: %v", m)
	}
	if got, _ := Slice(m, "Binds"); len(got) != 1 || got[0] != "new" {
		t.Errorf("value not replaced: %v", m)
	}
}

func TestSetFieldAddsWhenAbsent(t *testing.T) {
	m := map[string]any{}
	SetField(m, "Binds", []any{"x"})
	if _, ok := m["Binds"]; !ok {
		t.Errorf("SetField did not add the field: %v", m)
	}
}

func TestAccessorsTolerateNil(t *testing.T) {
	if _, ok := Field(nil, "x"); ok {
		t.Error("Field(nil) reported a hit")
	}
	if String(nil, "x") != "" {
		t.Error("String(nil) returned a value")
	}
	SetField(nil, "x", 1) // must not panic
}
