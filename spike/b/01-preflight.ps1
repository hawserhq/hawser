# Spike B step 1: baseline in the interactive session, and set up a distro to probe.
# Establishes what "working" looks like before any service is involved, so a later
# failure can be attributed to session 0 rather than to the environment.
$ErrorActionPreference = 'Stop'
$here = $PSScriptRoot
$distro = 'skrog-spike-b'

function Assert-Admin {
    $id = [Security.Principal.WindowsIdentity]::GetCurrent()
    $p = [Security.Principal.WindowsPrincipal]::new($id)
    if (-not $p.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
        throw "run this from an elevated PowerShell - service and account operations need it"
    }
}
Assert-Admin

Write-Host "== interactive baseline ==" -ForegroundColor Cyan
"whoami: $(whoami)"
"session: $((Get-Process -Id $PID).SessionId)   (0 = services session)"
(wsl --version) -replace "`0","" | Select-Object -First 2
Write-Host "`ndistros visible to $(whoami):"
(wsl --list --verbose) -replace "`0","" | Where-Object { $_ -match '\S' }

# Reuse Spike A's rootfs recipe: Alpine + static dockerd. If Spike A's download
# is still around, use it; otherwise fetch.
$dl = Join-Path (Split-Path $here -Parent) 'a\dl'
$alpine = Join-Path $dl 'alpine-minirootfs-3.24.1-x86_64.tar.gz'
$docker = Join-Path $dl 'docker-29.7.2.tgz'
if (-not (Test-Path $alpine) -or -not (Test-Path $docker)) {
    Write-Host "`nfetching rootfs inputs (reusing spike/a/01-fetch.ps1)..." -ForegroundColor Cyan
    & (Join-Path (Split-Path $here -Parent) 'a\01-fetch.ps1')
}

if ((wsl --list --quiet) -replace "`0","" | Where-Object { $_.Trim() -eq $distro }) {
    Write-Host "`ndistro $distro already registered - skipping import" -ForegroundColor Yellow
} else {
    Write-Host "`n== importing $distro (as the interactive user) ==" -ForegroundColor Cyan
    # Deliberately imported as the interactive user: whether a service account
    # can see this registration is the question the spike exists to answer.
    $vhd = Join-Path $env:LOCALAPPDATA $distro
    New-Item -ItemType Directory -Force $vhd | Out-Null
    wsl --import $distro $vhd $alpine --version 2
    if ($LASTEXITCODE -ne 0) { throw "wsl --import failed" }

    $setup = (wsl -d $distro --exec wslpath -a (Join-Path (Split-Path $here -Parent) 'a\03-setup.sh')).Trim()
    $tgz = (wsl -d $distro --exec wslpath -a $docker).Trim()
    wsl -d $distro -u root --exec sh $setup $tgz
    if ($LASTEXITCODE -ne 0) { throw "distro setup failed" }
}

Write-Host "`n== console-mode probe (interactive baseline) ==" -ForegroundColor Cyan
Push-Location (Join-Path $here 'agent')
go build -o probe.exe .
if ($LASTEXITCODE -ne 0) { Pop-Location; throw "probe build failed" }
Pop-Location

$env:SKROG_SPIKE_DISTRO = $distro
& (Join-Path $here 'agent\probe.exe')

Write-Host "`nBaseline recorded. Every check below should be OK before continuing:" -ForegroundColor Cyan
& (Join-Path $here 'show-log.ps1')
Write-Host "`nNext: .\02-service-localsystem.ps1" -ForegroundColor Green
