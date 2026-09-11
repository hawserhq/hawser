// Package hostca reads the Windows host's trusted root CA store, so Skrog can
// (opt-in) trust the same roots inside the engine — the fix for a corporate
// TLS-inspecting proxy whose root the engine does not know (#62).
//
// Reading the Root store needs no elevation: it is a machine-readable store.
package hostca

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// readScript exports every certificate in the LocalMachine and CurrentUser Root
// stores as a concatenated PEM bundle. Both are read because corporate CAs may
// be deployed to either.
const readScript = `
$ErrorActionPreference = 'Stop'
foreach ($loc in 'LocalMachine','CurrentUser') {
  try {
    $st = New-Object System.Security.Cryptography.X509Certificates.X509Store('Root',$loc)
    $st.Open('ReadOnly')
    foreach ($c in $st.Certificates) {
      '-----BEGIN CERTIFICATE-----'
      [Convert]::ToBase64String($c.RawData,'InsertLineBreaks')
      '-----END CERTIFICATE-----'
    }
    $st.Close()
  } catch { }
}
`

// HostRootCAs returns the host's trusted root certificates as a PEM bundle.
func HostRootCAs(ctx context.Context) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", readScript)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("reading the host root CA store: %w", err)
	}
	if !strings.Contains(string(out), "BEGIN CERTIFICATE") {
		return nil, fmt.Errorf("the host root CA store returned no certificates")
	}
	return out, nil
}
