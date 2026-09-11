package doctor

import (
	"testing"

	"github.com/hawserhq/hawser/internal/version"
)

func report(r version.Report) *version.Report { return &r }

func TestCheckDockerCLI(t *testing.T) {
	hawserFirst := []version.Binary{
		{Path: `C:\hawser\docker.exe`, Origin: version.OriginHawser, First: true},
	}
	ddFirst := []version.Binary{
		{Path: `C:\dd\docker.exe`, Origin: version.OriginDockerDesktop, First: true},
		{Path: `C:\hawser\docker.exe`, Origin: version.OriginHawser},
	}

	cases := []struct {
		name string
		f    Facts
		want Status
	}{
		{"none on path", Facts{Report: report(version.Report{})}, Fail},
		{"hawser first", Facts{Report: report(version.Report{Docker: hawserFirst, Context: "hawser"})}, OK},
		{"shadowed by docker desktop", Facts{Report: report(version.Report{Docker: ddFirst, Context: "hawser"})}, Warn},
		{"foreign first but not hawser context", Facts{Report: report(version.Report{Docker: ddFirst, Context: "default"})}, OK},
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
