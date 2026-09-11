# Skrog's one-line installer (#77).
#
#   irm https://wslkit.github.io/skrog/install.ps1 | iex
#
# Downloads a pinned release, verifies it against the release's SHA256SUMS,
# unpacks it, and puts it on your PATH. It does NOT install the engine: that
# provisions a WSL2 distro and downloads a rootfs, which is not something a
# one-liner should do without being asked. `skrog install` is the next step,
# and this script says so when it finishes.
#
# Options, because `irm | iex` cannot pass arguments, come from the environment:
#
#   $env:SKROG_VERSION = '0.3.0'          # default: the newest release
#   $env:SKROG_DIR     = 'C:\tools\skrog' # default: %LOCALAPPDATA%\Programs\skrog
#   $env:SKROG_NO_PATH = '1'               # do not touch PATH
#
# Invoked as a file or a script block, the parameters below work normally:
#
#   & ([scriptblock]::Create((irm https://wslkit.github.io/skrog/install.ps1))) -Version 0.3.0
#
# A CI runner should use the action instead: https://github.com/wslkit/setup-skrog
# It shares this script's release-resolution and verification logic but is
# built for unattended use, with its own inputs and outputs.
[CmdletBinding()]
param(
    [string]$Version = $env:SKROG_VERSION,
    [string]$Dir = $env:SKROG_DIR,
    [switch]$NoPath = [bool]$env:SKROG_NO_PATH
)

$ErrorActionPreference = 'Stop'
# Native tools report failure through exit codes here, not exceptions.
$PSNativeCommandUseErrorActionPreference = $false
$repo = 'wslkit/skrog'

function Say([string]$m) { Write-Host $m }
function Step([string]$m) { Write-Host "  $m" -ForegroundColor DarkGray }

# --- 1. resolve the release --------------------------------------------------

$Version = ($Version -replace '^v', '').Trim()
if (-not $Version) {
    # The repository publishes app releases (v0.3.0) and rootfs releases
    # (rootfs-v29.8.0-1) into one tag namespace, and /releases/latest can
    # return either. "Newest" here means the newest published app release: a
    # v<semver> tag that is not a draft. Every pre-1.0 release is flagged
    # prerelease, so a stable one wins when it exists without excluding one.
    $rels = Invoke-RestMethod -Headers @{ 'User-Agent' = 'skrog-install' } `
        "https://api.github.com/repos/$repo/releases?per_page=50"
    $apps = @($rels | Where-Object { $_.tag_name -match '^v\d+\.\d+\.\d+$' -and -not $_.draft } |
        Sort-Object { [version]($_.tag_name -replace '^v', '') } -Descending)
    $app = @($apps | Where-Object { -not $_.prerelease })[0]
    if (-not $app) { $app = $apps[0] }
    if (-not $app) { throw "no published skrog release found in $repo" }
    $Version = $app.tag_name -replace '^v', ''
}

$arch = if ([Runtime.InteropServices.RuntimeInformation]::OSArchitecture -eq 'Arm64') { 'arm64' } else { 'amd64' }
$zip = "skrog_${Version}_windows_$arch.zip"
$base = "https://github.com/$repo/releases/download/v$Version"

Say ""
Say "Skrog $Version ($arch)"

# --- 2. download and verify --------------------------------------------------

$work = Join-Path ([IO.Path]::GetTempPath()) "skrog-install-$Version"
New-Item -ItemType Directory -Force $work | Out-Null
$zipPath = Join-Path $work $zip
$sumsPath = Join-Path $work 'SHA256SUMS'

try {
    Step "downloading $zip"
    Invoke-WebRequest "$base/$zip" -OutFile $zipPath
    Invoke-WebRequest "$base/SHA256SUMS" -OutFile $sumsPath

    # `<hash>  <file>` per line; a leading * (binary mode) is allowed.
    $entry = Get-Content $sumsPath |
        Where-Object { $_ -match "\s+\*?$([regex]::Escape($zip))\s*$" } |
        Select-Object -First 1
    if (-not $entry) { throw "SHA256SUMS has no entry for $zip" }
    $expected = ($entry -split '\s+')[0].ToLower()
    $actual = (Get-FileHash $zipPath -Algorithm SHA256).Hash.ToLower()
    if ($actual -ne $expected) {
        throw "checksum mismatch for ${zip}: expected $expected, got $actual"
    }
    Step "verified sha256 $actual"

    # --- 3. unpack -----------------------------------------------------------

    if (-not $Dir) { $Dir = Join-Path $env:LOCALAPPDATA 'Programs\skrog' }
    # Extracting over a running skrog.exe fails with a file lock, which is a
    # better error than a half-replaced install, but a useless one on its own.
    $running = Get-Process -Name skrog, skrogw, skrogtray -ErrorAction SilentlyContinue |
        Where-Object { $_.Path -and $_.Path.StartsWith($Dir, [StringComparison]::OrdinalIgnoreCase) }
    if ($running) {
        throw ("Skrog is running from $Dir (" + (($running.Name | Sort-Object -Unique) -join ', ') + "). " +
            "Stop it first: skrog stop; then quit the tray if it is open.")
    }

    New-Item -ItemType Directory -Force $Dir | Out-Null
    Expand-Archive $zipPath -DestinationPath $Dir -Force
    $exe = Join-Path $Dir 'skrog.exe'
    if (-not (Test-Path $exe)) { throw "skrog.exe not found after extracting $zip" }
    Step "unpacked to $Dir"
} finally {
    Remove-Item -Recurse -Force $work -ErrorAction SilentlyContinue
}

# --- 4. PATH -----------------------------------------------------------------

# Read and write the raw user PATH, never the expanded process one. Writing
# back an expanded value is the classic way an installer turns %USERPROFILE%
# in someone's PATH into a literal path that breaks when the profile moves.
function Add-ToUserPath([string]$dir) {
    $key = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey('Environment', $true)
    try {
        $raw = $key.GetValue('Path', '', 'DoNotExpandEnvironmentNames')
        $kind = if ($raw -match '%') { 'ExpandString' } else { 'String' }
        $parts = @($raw -split ';' | Where-Object { $_ })
        if ($parts -contains $dir) { return $false }
        $key.SetValue('Path', (($parts + $dir) -join ';'), $kind)
        return $true
    } finally {
        $key.Dispose()
    }
}

$added = $false
if (-not $NoPath) {
    $added = Add-ToUserPath $Dir
    $env:Path = "$Dir;$env:Path"
}

# --- 5. what now -------------------------------------------------------------

# `skrog version` exits 3 when no engine is installed, which is the expected
# state here and not a failure.
# Collapsed: the version report is tab-aligned, which reads as a gap here.
$reported = ((& $exe --version 2>$null | Select-Object -First 1) -replace '\s+', ' ').Trim()

Say ""
Say "Installed $(if ($reported) { $reported } else { "skrog $Version" })"
Say "  $exe"
if ($added) {
    Say "  added to your PATH (open a new terminal to pick it up)"
} elseif ($NoPath) {
    Say "  PATH not modified (SKROG_NO_PATH)"
} else {
    Say "  already on your PATH"
}

Say ""
Say "Next:"
Say "  skrog install      provision the engine (downloads a verified rootfs, ~2 min)"
Say "  skrog doctor       check this machine is ready first"
Say ""
Say "Binaries are not signed yet, so Windows SmartScreen may warn on first run."
Say "Docs: https://wslkit.github.io/skrog/"
Say ""
