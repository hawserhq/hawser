# Install the local dev tools CI depends on, without needing admin or Docker.
# Everything lands in %LOCALAPPDATA%\skrog-tools, which scripts/lint.ps1 checks.
#
#   pwsh -File scripts/install-tools.ps1
$ErrorActionPreference = 'Stop'

$tools = Join-Path $env:LOCALAPPDATA 'skrog-tools'
New-Item -ItemType Directory -Force $tools | Out-Null

# --- shellcheck (matches the CI job) ---------------------------------------
if (Get-Command shellcheck -ErrorAction SilentlyContinue) {
    Write-Host "shellcheck: already on PATH" -ForegroundColor Green
} elseif (Test-Path (Join-Path $tools 'shellcheck.exe')) {
    Write-Host "shellcheck: already in $tools" -ForegroundColor Green
} else {
    Write-Host "shellcheck: downloading..."
    $zip = Join-Path $env:TEMP 'shellcheck.zip'
    $ext = Join-Path $env:TEMP 'shellcheck-extract'
    Invoke-WebRequest 'https://github.com/koalaman/shellcheck/releases/download/stable/shellcheck-stable.zip' -OutFile $zip
    Expand-Archive $zip -DestinationPath $ext -Force
    $exe = Get-ChildItem -Recurse $ext -Filter 'shellcheck*.exe' | Select-Object -First 1
    Copy-Item $exe.FullName (Join-Path $tools 'shellcheck.exe') -Force
    Remove-Item $zip, $ext -Recurse -Force -ErrorAction SilentlyContinue
    Write-Host "shellcheck: installed to $tools" -ForegroundColor Green
}

# --- git hooks --------------------------------------------------------------
git config core.hooksPath .githooks
Write-Host "git hooks: core.hooksPath -> .githooks" -ForegroundColor Green

# --- Go (informational; the installer needs a normal winget run) -----------
if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    Write-Host "go: not installed - run 'winget install GoLang.Go', then reopen your terminal" -ForegroundColor Yellow
} else {
    Write-Host "go: $(go version)" -ForegroundColor Green
}

Write-Host "`nDone. Run scripts/lint.ps1 before committing." -ForegroundColor Cyan
