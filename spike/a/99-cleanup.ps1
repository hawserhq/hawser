# Spike A cleanup: remove every trace of the spike.
$ErrorActionPreference = 'Continue'

wsl --terminate skrog-spike 2>$null
wsl --unregister skrog-spike 2>$null
Remove-Item -Recurse -Force (Join-Path $env:LOCALAPPDATA 'skrog-spike') -ErrorAction SilentlyContinue
Remove-Item -Recurse -Force (Join-Path $PSScriptRoot 'dl') -ErrorAction SilentlyContinue
# Only build output — go.sum is tracked in git.
Remove-Item -Force (Join-Path $PSScriptRoot 'relay\relay.exe') -ErrorAction SilentlyContinue

Write-Host "skrog-spike distro unregistered, downloads and binaries removed."
