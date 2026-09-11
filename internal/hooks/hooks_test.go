package hooks

import "testing"

func TestFilterInjectedIgnoresWindowsAndOurOwnDirectory(t *testing.T) {
	mods := []Module{
		{Name: "skrog.exe", Path: `C:\Skrog\bin\skrog.exe`},
		{Name: "KERNEL32.DLL", Path: `C:\Windows\System32\KERNEL32.DLL`},
		{Name: "ucrtbase.dll", Path: `C:\Windows\System32\ucrtbase.dll`},
		{Name: "wow64.dll", Path: `C:\Windows\SysWOW64\wow64.dll`},
		{Name: "sxs.dll", Path: `C:\Windows\WinSxS\x86_something\sxs.dll`},
		{Name: "skrogw.exe", Path: `C:\Skrog\bin\skrogw.exe`},
		{Name: "InProcessClient64.dll", Path: `C:\Program Files\SomeEDR\Agent 1.2\InProcessClient64.dll`},
		{Name: "noPath.dll", Path: ""},
	}
	got := filterInjected(mods, `C:\Skrog\bin`)
	if len(got) != 1 {
		t.Fatalf("got %d modules, want 1: %+v", len(got), got)
	}
	if got[0].Name != "InProcessClient64.dll" {
		t.Errorf("flagged the wrong module: %+v", got[0])
	}
}

func TestFilterInjectedIsCaseAndSeparatorInsensitive(t *testing.T) {
	mods := []Module{
		{Name: "kernel32.dll", Path: `c:/WINDOWS/system32/kernel32.dll`},
		{Name: "self.dll", Path: `C:/Skrog/Bin/self.dll`},
		{Name: "hook.dll", Path: `C:\Vendor\hook.dll`},
	}
	got := filterInjected(mods, `c:\skrog\bin`)
	if len(got) != 1 || got[0].Name != "hook.dll" {
		t.Errorf("got %+v, want only hook.dll", got)
	}
}

func TestFilterInjectedWithoutAnExecutableDir(t *testing.T) {
	// os.Executable can fail; the classification must still work, just without
	// the "beside the exe" exemption.
	mods := []Module{{Name: "hook.dll", Path: `C:\Vendor\hook.dll`}}
	if got := filterInjected(mods, ""); len(got) != 1 {
		t.Errorf("got %+v, want the module reported", got)
	}
}

func TestInjectedDoesNotPanic(t *testing.T) {
	// Whatever this machine has loaded, reading our own module list must not
	// fail: doctor calls it on every run.
	for _, m := range Injected() {
		if m.Name == "" && m.Path == "" {
			t.Error("an empty module made it through")
		}
	}
}
