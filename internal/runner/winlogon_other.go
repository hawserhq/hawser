//go:build !windows

package runner

import "errors"

// Winlogon mirrors the Windows type so the package compiles on the Linux CI
// runner that vets the codebase; auto-logon is a Windows concept.
type Winlogon struct {
	AutoAdminLogon     string
	DefaultUserName    string
	DefaultDomainName  string
	HasDefaultPassword bool
}

// Configured is the playbook's definition: AutoAdminLogon on and an account named.
func (w Winlogon) Configured() bool {
	return w.AutoAdminLogon == "1" && w.DefaultUserName != ""
}

var errNotWindows = errors.New("auto-logon settings exist only on Windows")

func ReadWinlogon() (Winlogon, error)       { return Winlogon{}, errNotWindows }
func CurrentAccount() (name, domain string) { return "", "" }
