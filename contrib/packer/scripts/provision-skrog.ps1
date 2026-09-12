<#
.SYNOPSIS
  Packer provisioner: install a pinned Skrog and pre-import the engine into a
  runner image (#143). Run as the runner's account; Skrog's state and autostart
  are per-user.

.DESCRIPTION
  Uses setup-skrog's install script (pinned by tag) for the verified download,
  so the image, the GitHub Action, and GitLab all install Skrog the same way.
  Then imports the engine -- offline from an air-gap bundle when SKROG_BUNDLE
  is set (#75), else the pinned rootfs download -- registers the logon
  autostart, and gates the image on `skrog doctor` failing nothing.

  Environment (set by the Packer template):
    SKROG_VERSION      release to install (required)
    SKROG_BUNDLE       path to a `skrog bundle` zip, or empty
    SKROG_LOCKFILE     path to a skrog.lock, or empty
    SETUP_SKROG_REF    tag of wslkit/setup-skrog to take the script from
    SKROG_RUNNER_USER  documentation only: the account this image logs on as
#>
$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $false

$version = $env:SKROG_VERSION
if (-not $version) { throw 'SKROG_VERSION is required (never bake "latest" into an image)' }
$ref = if ($env:SETUP_SKROG_REF) { $env:SETUP_SKROG_REF } else { 'v2' }

# 1. Stage skrog.exe, verified against SHA256SUMS, into a machine-wide-readable
#    place (the runner account and the provisioning account may differ).
$dest = 'C:\Skrog'
$script = Join-Path $env:TEMP 'install-skrog.ps1'
Invoke-WebRequest "https://raw.githubusercontent.com/wslkit/setup-skrog/$ref/scripts/install-skrog.ps1" -OutFile $script
pwsh -NoProfile -File $script -Version $version -Install:$false -Dest $dest
if ($LASTEXITCODE -ne 0) { throw "staging skrog $version failed ($LASTEXITCODE)" }
$skrog = Join-Path $dest 'bin\skrog.exe'

# 2. Put it on the machine PATH so every account (and the runner service's
#    session) finds it. Machine scope needs the elevated Packer session, which
#    is where this runs.
$machinePath = [Environment]::GetEnvironmentVariable('Path', 'Machine')
if ($machinePath -notlike "*$dest\bin*") {
  [Environment]::SetEnvironmentVariable('Path', "$dest\bin;$machinePath", 'Machine')
}

# 3. Import the engine. Offline from the bundle when provided: no network, no
#    proxy/CA dance, identical bytes on every image.
$installArgs = @('install', '--headless')
if ($env:SKROG_BUNDLE) {
  $installArgs += @('--offline', $env:SKROG_BUNDLE)
} elseif ($env:SKROG_LOCKFILE) {
  $installArgs += @('--locked', $env:SKROG_LOCKFILE)
}
& $skrog @installArgs
if ($LASTEXITCODE -ne 0) { throw "skrog install failed ($LASTEXITCODE)" }

# 4. Autostart is registered by install (unless --no-autostart); confirm.
& $skrog autostart status
if ($LASTEXITCODE -ne 0) { throw 'autostart is not registered; the runner would boot without an engine' }

# 5. Gate the image: doctor must fail nothing. Warnings are printed, not fatal
#    -- e.g. the runner-setup check will warn until auto-logon is configured,
#    which is deliberately left to your own provisioner (see the template).
& $skrog doctor --json | Out-Host
if ($LASTEXITCODE -ne 0) { throw 'skrog doctor reports a failure; refusing to bake a broken image' }

# 6. Stop the engine so the image is captured quiescent; autostart brings it
#    back at first logon.
& $skrog stop
Write-Host "skrog $version baked; engine imported; autostart registered"
