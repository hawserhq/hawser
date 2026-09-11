# Spike A step 2: import the distro and provision dockerd inside it.
$ErrorActionPreference = 'Stop'
$dl     = Join-Path $PSScriptRoot 'dl'
$alpine = Join-Path $dl 'alpine-minirootfs-3.24.1-x86_64.tar.gz'
$tgz    = Join-Path $dl 'docker-29.7.2.tgz'
$vhdDir = Join-Path $env:LOCALAPPDATA 'skrog-spike'

if ((wsl --list --quiet) -contains 'skrog-spike') { throw "distro 'skrog-spike' already exists - run 99-cleanup.ps1 first" }

Write-Host "Importing skrog-spike ..."
New-Item -ItemType Directory -Force $vhdDir | Out-Null
wsl --import skrog-spike $vhdDir $alpine --version 2
if ($LASTEXITCODE -ne 0) { throw "wsl --import failed" }

# Translate Windows paths for use inside the distro, then run the setup script.
$setupWsl = (wsl -d skrog-spike --exec wslpath -a ($PSScriptRoot + '\03-setup.sh')).Trim()
$tgzWsl   = (wsl -d skrog-spike --exec wslpath -a $tgz).Trim()
Write-Host "Running 03-setup.sh inside the distro ..."
wsl -d skrog-spike -u root --exec sh $setupWsl $tgzWsl
if ($LASTEXITCODE -ne 0) { throw "03-setup.sh failed" }

Write-Host "Done. Next: .\04-start-engine.ps1"
