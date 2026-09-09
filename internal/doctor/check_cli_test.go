package doctor

import "testing"

func TestCheckCLI(t *testing.T) {
	bin := `C:\Users\me\AppData\Local\Hawser\bin`
	cases := []struct {
		name string
		cli  CLIStatus
		want Status
	}{
		{"not installed", CLIStatus{Installed: false}, Skip},
		{
			"installed and active",
			CLIStatus{Installed: true, BinDir: bin, OnPath: true, ActiveDocker: bin + `\docker.exe`},
			OK,
		},
		{
			"installed, active is a case variant of the bundle",
			CLIStatus{Installed: true, BinDir: bin, OnPath: true, ActiveDocker: `c:\users\me\appdata\local\hawser\bin\docker.exe`},
			OK,
		},
		{
			"installed but not on PATH",
			CLIStatus{Installed: true, BinDir: bin, OnPath: false, ActiveDocker: ""},
			Warn,
		},
		{
			"installed but Docker Desktop shadows it",
			CLIStatus{Installed: true, BinDir: bin, OnPath: true, ActiveDocker: `C:\Program Files\Docker\Docker\resources\bin\docker.exe`},
			Warn,
		},
	}
	c := checkCLI()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := c.Run(Facts{CLI: tc.cli}).Status; got != tc.want {
				t.Fatalf("status = %v, want %v", got, tc.want)
			}
		})
	}
}
