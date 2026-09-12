//go:build !windows

package runner

// ReadPower is a Windows concept; the Linux CI runner that vets this codebase
// only needs it to compile.
func ReadPower() PowerFacts {
	return PowerFacts{Reason: "power settings exist only on Windows"}
}
