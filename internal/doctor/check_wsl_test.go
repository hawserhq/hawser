package doctor

import (
	"context"
	"testing"

	"github.com/hawserhq/hawser/internal/wsl"
)

func TestCheckWSL(t *testing.T) {
	cases := []struct {
		name string
		f    Facts
		want Status
	}{
		{"not installed", Facts{WSL: wsl.Status{Installed: false}}, Fail},
		{"query error", Facts{WSLErr: "boom"}, Fail},
		{"default version 1", Facts{WSL: wsl.Status{Installed: true, DefaultVersion: 1, Version: "2.7.8"}}, Warn},
		{"no version string", Facts{WSL: wsl.Status{Installed: true, DefaultVersion: 2, Version: ""}}, Warn},
		{"too old", Facts{WSL: wsl.Status{Installed: true, DefaultVersion: 2, Version: "1.5.0"}}, Warn},
		{"healthy", Facts{WSL: wsl.Status{Installed: true, DefaultVersion: 2, Version: "2.7.8"}}, OK},
	}
	c := checkWSL()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := c.Run(tc.f).Status; got != tc.want {
				t.Fatalf("status = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCheckWSLFixIsNoOpWhenDefaultIsTwo(t *testing.T) {
	c := checkWSL()
	done, err := c.Fix(context.Background(), Facts{WSL: wsl.Status{Installed: true, DefaultVersion: 2}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if done != "" {
		t.Fatalf("expected no-op fix, got %q", done)
	}
}
