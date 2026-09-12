//go:build windows

package runner

import "testing"

// The AC index is what matters and the DC line follows it, so a bare search
// for "Power Setting Index" would silently read the battery timeout instead —
// a misread that looks exactly like a correct reading. This is real
// `powercfg /q SCHEME_CURRENT SUB_SLEEP STANDBYIDLE` output.
func TestACIndexIsTakenNotDC(t *testing.T) {
	const out = `
Power Scheme GUID: 381b4222-f694-41f0-9685-ff5bb260df2e  (Balanced)
  Subgroup GUID: 238c9fa8-0aad-41ed-83f4-97be242c8f20  (Sleep)
    Power Setting GUID: 29f6c1db-86da-48c5-9fdb-f2b67b1f44da  (Sleep after)
      Minimum Possible Setting: 0x00000000
      Maximum Possible Setting: 0xffffffff
      Current AC Power Setting Index: 0x00000e10
      Current DC Power Setting Index: 0x00000384
`
	m := acIndexRe.FindStringSubmatch(out)
	if m == nil {
		t.Fatal("no AC index found in real powercfg output")
	}
	if m[1] != "00000e10" {
		t.Errorf("matched %q; 00000384 would mean the DC (battery) value was read instead", m[1])
	}
}

// A machine with sleep disabled reports zero, which must parse as zero rather
// than as a failure to read.
func TestZeroIndexParsesAsNever(t *testing.T) {
	const out = "      Current AC Power Setting Index: 0x00000000\n"
	m := acIndexRe.FindStringSubmatch(out)
	if m == nil || m[1] != "00000000" {
		t.Fatalf("zero index not matched: %v", m)
	}
}

// Output with no AC line at all must be an error, not a silent zero — "never
// sleeps" is the reassuring answer and must never be inferred from absence.
func TestMissingACLineIsNotReadAsZero(t *testing.T) {
	if m := acIndexRe.FindStringSubmatch("Subgroup GUID: ...\n"); m != nil {
		t.Errorf("matched something in output with no AC line: %v", m)
	}
}
