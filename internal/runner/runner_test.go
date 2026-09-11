package runner

import "testing"

func byName(fs []Finding) map[string]Finding {
	m := map[string]Finding{}
	for _, f := range fs {
		m[f.Name] = f
	}
	return m
}

func healthy() Facts {
	return Facts{
		AutoLogonConfigured: true, AutoLogonUser: "skrog-runner", AutoLogonDomain: "BUILD01",
		CurrentUser: "skrog-runner", CurrentDomain: "BUILD01",
		AutostartRegistered: true, SupervisorRunning: true, Engine: "running",
	}
}

func TestEvaluateHealthyRunner(t *testing.T) {
	fs := Evaluate(healthy())
	if !Ready(fs) {
		t.Fatalf("healthy runner should be ready: %+v", fs)
	}
	for _, f := range fs {
		if f.Status != OK {
			t.Errorf("%s = %s (%s)", f.Name, f.Status, f.Summary)
		}
		if f.Status == OK && f.Remedy != "" {
			t.Errorf("%s is OK but carries a remedy", f.Name)
		}
	}
	// An idle engine is a fine place for a runner to sit between jobs.
	f := healthy()
	f.Engine = "idle"
	if !Ready(Evaluate(f)) {
		t.Error("idle engine must count as ready")
	}
}

func TestEvaluateFindsEachProblem(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Facts)
		finding string
		want    Status
	}{
		{"no auto-logon", func(f *Facts) { f.AutoLogonConfigured = false }, "autologon", Fail},
		{"wrong account", func(f *Facts) { f.AutoLogonUser = "someone-else" }, "autologon-account", Fail},
		{"wrong domain", func(f *Facts) { f.AutoLogonDomain = "OTHER" }, "autologon-account", Fail},
		{"plaintext password", func(f *Facts) { f.PlaintextPassword = true }, "autologon-password", Warn},
		{"no autostart", func(f *Facts) { f.AutostartRegistered = false }, "autostart", Fail},
		{"supervisor down", func(f *Facts) { f.SupervisorRunning = false }, "supervisor", Fail},
		{"engine stopped", func(f *Facts) { f.Engine = "stopped" }, "engine", Fail},
		{"not installed", func(f *Facts) { f.Engine = "not-installed" }, "engine", Fail},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := healthy()
			tc.mutate(&f)
			got := byName(Evaluate(f))[tc.finding]
			if got.Status != tc.want {
				t.Fatalf("%s = %s, want %s (%+v)", tc.finding, got.Status, tc.want, got)
			}
			if got.Remedy == "" {
				t.Errorf("%s must carry a remedy", tc.finding)
			}
		})
	}
}

func TestWarningsDoNotBlockReadiness(t *testing.T) {
	f := healthy()
	f.PlaintextPassword = true
	fs := Evaluate(f)
	if !Ready(fs) {
		t.Error("a clear-text password is a warning, not a blocker")
	}
}

func TestSameAccountRules(t *testing.T) {
	cases := []struct {
		name  string
		f     Facts
		equal bool
	}{
		{"case-insensitive user", Facts{AutoLogonUser: "Runner", CurrentUser: "runner"}, true},
		{"different user", Facts{AutoLogonUser: "a", CurrentUser: "b"}, false},
		{"dot means local machine", Facts{AutoLogonUser: "r", CurrentUser: "r", AutoLogonDomain: ".", CurrentDomain: "BUILD01"}, true},
		{"unknown domain tolerated", Facts{AutoLogonUser: "r", CurrentUser: "r", AutoLogonDomain: "", CurrentDomain: "BUILD01"}, true},
		{"domains differ", Facts{AutoLogonUser: "r", CurrentUser: "r", AutoLogonDomain: "CORP", CurrentDomain: "BUILD01"}, false},
		{"domains equal ignoring case", Facts{AutoLogonUser: "r", CurrentUser: "r", AutoLogonDomain: "corp", CurrentDomain: "CORP"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sameAccount(tc.f); got != tc.equal {
				t.Fatalf("sameAccount = %v, want %v", got, tc.equal)
			}
		})
	}
}

// The account name is compared, never printed: no finding text may contain it.
func TestFindingsNeverLeakTheAccountName(t *testing.T) {
	f := healthy()
	f.AutoLogonUser, f.CurrentUser = "secret-runner-name", "other-name"
	for _, fd := range Evaluate(f) {
		for _, s := range []string{fd.Summary, fd.Remedy} {
			if contains(s, "secret-runner-name") || contains(s, "other-name") {
				t.Errorf("finding %s leaks an account name: %q", fd.Name, s)
			}
		}
	}
}

func contains(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
