<#
.SYNOPSIS
  Packer provisioner: install a pinned Hawser and pre-import the engine into a
  runner image (#143). Run as the runner's account; Hawser's state and autostart
  are per-user.

.DESCRIPTION
  Uses setup-hawser's install script (pinned by tag) for the verified download,
  so the image, the GitHub Action, and GitLab all install Hawser the same way.
  Then imports the engine -- offline from an air-gap bundle when HAWSER_BUNDLE
  is set (#75), else the pinned rootfs download -- registers the logon
  autostart, and gates the image on `hawser doctor` failing nothing.

  Environment (set by the Packer template):
    HAWSER_VERSION      release to install (required)
    HAWSER_BUNDLE       path to a `hawser bundle` zip, or empty
    HAWSER_LOCKFILE     path to a hawser.lock, or empty
    SETUP_HAWSER_REF    tag of hawserhq/setup-hawser to take the script from
    HAWSER_RUNNER_USER  documentation only: the account this image logs on as
#>
$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $false

$version = $env:HAWSER_VERSION
if (-not $version) { throw 'HAWSER_VERSION is required (never bake "latest" into an image)' }
$ref = if ($env:SETUP_HAWSER_REF) { $env:SETUP_HAWSER_REF } else { 'v1' }

# 1. Stage hawser.exe, verified against SHA256SUMS, into a machine-wide-readable
#    place (the runner account and the provisioning account may differ).
$dest = 'C:\Hawser'
$script = Join-Path $env:TEMP 'install-hawser.ps1'
Invoke-WebRequest "https://raw.githubusercontent.com/hawserhq/setup-hawser/$ref/scripts/install-hawser.ps1" -OutFile $script
pwsh -NoProfile -File $script -Version $version -Install:$false -Dest $dest
if ($LASTEXITCODE -ne 0) { throw "staging hawser $version failed ($LASTEXITCODE)" }
$hawser = Join-Path $dest 'bin\hawser.exe'

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
if ($env:HAWSER_BUNDLE) {
  $installArgs += @('--offline', $env:HAWSER_BUNDLE)
} elseif ($env:HAWSER_LOCKFILE) {
  $installArgs += @('--locked', $env:HAWSER_LOCKFILE)
}
& $hawser @installArgs
if ($LASTEXITCODE -ne 0) { throw "hawser install failed ($LASTEXITCODE)" }

# 4. Autostart is registered by install (unless --no-autostart); confirm.
& $hawser autostart status
if ($LASTEXITCODE -ne 0) { throw 'autostart is not registered; the runner would boot without an engine' }

# 5. Gate the image: doctor must fail nothing. Warnings are printed, not fatal
#    -- e.g. the runner-setup check will warn until auto-logon is configured,
#    which is deliberately left to your own provisioner (see the template).
& $hawser doctor --json | Out-Host
if ($LASTEXITCODE -ne 0) { throw 'hawser doctor reports a failure; refusing to bake a broken image' }

# 6. Stop the engine so the image is captured quiescent; autostart brings it
#    back at first logon.
& $hawser stop
Write-Host "hawser $version baked; engine imported; autostart registered"
