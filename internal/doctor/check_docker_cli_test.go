package doctor

import (
	"testing"

	"github.com/wslkit/skrog/internal/version"
)

func report(r version.Report) *version.Report { return &r }

func TestCheckDockerCLI(t *testing.T) {
	skrogFirst := []version.Binary{
		{Path: `C:\skrog\docker.exe`, Origin: version.OriginSkrog, First: true},
	}
	ddFirst := []version.Binary{
		{Path: `C:\dd\docker.exe`, Origin: version.OriginDockerDesktop, First: true},
		{Path: `C:\skrog\docker.exe`, Origin: version.OriginSkrog},
	}

	cases := []struct {
		name string
		f    Facts
		want Status
	}{
		{"none on path", Facts{Report: report(version.Report{})}, Fail},
		{"skrog first", Facts{Report: report(version.Report{Docker: skrogFirst, Context: "skrog"})}, OK},
		{"shadowed by docker desktop", Facts{Report: report(version.Report{Docker: ddFirst, Context: "skrog"})}, Warn},
		{"foreign first but not skrog context", Facts{Report: report(version.Report{Docker: ddFirst, Context: "default"})}, OK},
	}
	c := checkDockerCLI()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := c.Run(tc.f).Status; got != tc.want {
				t.Fatalf("status = %v, want %v", got, tc.want)
			}
		})
	}
}
