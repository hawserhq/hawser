//go:build windows

package runner

import (
	"errors"
	"os"
	"os/user"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// winlogonKey is where Windows keeps auto-logon settings. Reading HKLM needs no
// elevation; only writing does.
const winlogonKey = `SOFTWARE\Microsoft\Windows NT\CurrentVersion\Winlogon`

// Winlogon is the auto-logon state as configured on the machine.
type Winlogon struct {
	// AutoAdminLogon is the raw value ("1" when on).
	AutoAdminLogon string
	// DefaultUserName / DefaultDomainName are the account auto-logon signs in.
	DefaultUserName   string
	DefaultDomainName string
	// HasDefaultPassword is whether a clear-text DefaultPassword value exists.
	// Its contents are never read: the probe asks only whether the value is
	// there, which is all the finding needs.
	HasDefaultPassword bool
}

// Configured is the playbook's definition: AutoAdminLogon on and an account named.
func (w Winlogon) Configured() bool {
	return strings.TrimSpace(w.AutoAdminLogon) == "1" && strings.TrimSpace(w.DefaultUserName) != ""
}

// ReadWinlogon reads the auto-logon settings.
func ReadWinlogon() (Winlogon, error) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, winlogonKey, registry.QUERY_VALUE)
	if err != nil {
		return Winlogon{}, err
	}
	defer k.Close()

	var w Winlogon
	w.AutoAdminLogon, _, _ = k.GetStringValue("AutoAdminLogon")
	w.DefaultUserName, _, _ = k.GetStringValue("DefaultUserName")
	w.DefaultDomainName, _, _ = k.GetStringValue("DefaultDomainName")

	// Existence only. GetValue with a nil buffer reports the size it would need
	// (ErrShortBuffer) when the value exists and ErrNotExist when it does not;
	// either way no bytes of the password land in this process.
	_, _, err = k.GetValue("DefaultPassword", nil)
	w.HasDefaultPassword = err == nil || errors.Is(err, registry.ErrShortBuffer)
	return w, nil
}

// CurrentAccount is the account running the process, as (user, domain).
func CurrentAccount() (name, domain string) {
	if u, err := user.Current(); err == nil && u.Username != "" {
		if i := strings.LastIndex(u.Username, `\`); i >= 0 {
			return u.Username[i+1:], u.Username[:i]
		}
		return u.Username, ""
	}
	return os.Getenv("USERNAME"), os.Getenv("USERDOMAIN")
}
