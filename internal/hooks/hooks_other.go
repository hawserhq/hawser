//go:build !windows

package hooks

// Injected has nothing to report off Windows: DLL injection is the mechanism
// being detected, and the whole package exists for one Windows failure mode.
func loadedModules() []Module { return nil }

func executableDir() string { return "" }
