package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLastLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "supervisor.log")
	content := "one\r\ntwo\nthree\nfour\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	lines, size, err := lastLines(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 || lines[0] != "three" || lines[1] != "four" {
		t.Errorf("last 2 = %q", lines)
	}
	// Size is the follow offset: the whole file, trailing newline included.
	if size != int64(len(content)) {
		t.Errorf("size = %d, want %d", size, len(content))
	}

	// 0 means everything; CR from CRLF files is stripped.
	lines, _, _ = lastLines(path, 0)
	if len(lines) != 4 || lines[0] != "one" {
		t.Errorf("all lines = %q", lines)
	}

	// A missing log is reported as such, so the caller can say "no log yet".
	if _, _, err := lastLines(filepath.Join(t.TempDir(), "nope.log"), 10); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing file error = %v, want ErrNotExist", err)
	}

	// Empty file: no lines, not one empty line.
	empty := filepath.Join(t.TempDir(), "empty.log")
	os.WriteFile(empty, nil, 0o644)
	if lines, _, _ := lastLines(empty, 10); len(lines) != 0 {
		t.Errorf("empty file gave %q", lines)
	}
}

func TestFormatLine(t *testing.T) {
	if got := formatLine("supervisor", "engine started", false); got != "engine started" {
		t.Errorf("plain = %q", got)
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(formatLine("dockerd", `time="x" level=info`, true)), &obj); err != nil {
		t.Fatalf("json line does not parse: %v", err)
	}
	if obj["source"] != "dockerd" || obj["line"] != `time="x" level=info` {
		t.Errorf("json = %v", obj)
	}
}
