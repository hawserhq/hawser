#Requires -Version 5.1
# Spike C end-to-end: build guest+host, run the probe, clean up.
param(
    [string]$Distro = 'Ubuntu',
    [int]$Port = 5000,
    [int]$Dials = 50
)
$ErrorActionPreference = 'Stop'
$here = Split-Path -Parent $MyInvocation.MyCommand.Path
$work = Join-Path $env:TEMP "skrog-spike-c"
New-Item -ItemType Directory -Force $work | Out-Null

Write-Host "== building guest (linux/amd64) and host probes"
Push-Location (Join-Path $here 'guest')
$env:GOOS = 'linux'; $env:GOARCH = 'amd64'
go build -o (Join-Path $work 'spikec-guest') .
if ($LASTEXITCODE -ne 0) { exit 1 }
Remove-Item Env:GOOS, Env:GOARCH
Pop-Location
Push-Location (Join-Path $here 'host')
go build -o (Join-Path $work 'spikec-host.exe') .
if ($LASTEXITCODE -ne 0) { exit 1 }
Pop-Location

Write-Host "== starting listener in $Distro on vsock:$Port"
$guestPath = (wsl -d $Distro -- wslpath -u (($work -replace '\','/') + '/spikec-guest')).Trim()
wsl -d $Distro -- sh -c "cp '$guestPath' /tmp/spikec-guest && chmod +x /tmp/spikec-guest && nohup /tmp/spikec-guest $Port > /tmp/spikec-guest.log 2>&1 & sleep 1; cat /tmp/spikec-guest.log"

try {
    Write-Host "== dialing from the host"
    & (Join-Path $work 'spikec-host.exe') -port $Port -n $Dials
    $result = $LASTEXITCODE
} finally {
    Write-Host "== cleaning up"
    wsl -d $Distro -- sh -c 'kill $(pgrep -x spikec-guest) 2>/dev/null; rm -f /tmp/spikec-guest /tmp/spikec-guest.log' | Out-Null
    Remove-Item -Recurse -Force $work
}
exit $result
