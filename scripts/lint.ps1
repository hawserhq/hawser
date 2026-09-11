# Run the same checks CI runs, before committing. No arguments: lints everything.
#
# shellcheck is found in whichever of these exists first — native install, the
# user's WSL distro, or the official container image — so nobody has to install
# anything to get the same answer CI gives.
[CmdletBinding()]
param([switch]$SkipGo)

$ErrorActionPreference = 'Stop'
$repo = Split-Path $PSScriptRoot -Parent
Push-Location $repo
try {
    $failed = @()

    # --- shellcheck -------------------------------------------------------
    # -x follows `source=` directives; -P SCRIPTDIR resolves them relative to
    # each script's own directory (versions.env sits next to build.sh).
    $shArgs = @('-x', '-P', 'SCRIPTDIR')
    $files  = Get-ChildItem -Recurse -Filter *.sh -File |
              ForEach-Object { [IO.Path]::GetRelativePath($repo, $_.FullName).Replace('\', '/') }

    if (-not $files) {
        Write-Host "shellcheck: no .sh files" -ForegroundColor DarkGray
    } else {
        $native = Get-Command shellcheck -ErrorAction SilentlyContinue
        if (-not $native) {
            # scripts/install-tools.ps1 puts it here when it isn't on PATH.
            $local = Join-Path $env:LOCALAPPDATA 'hawser-tools\shellcheck.exe'
            if (Test-Path $local) { $native = Get-Command $local }
        }
        $wslHas = $false
        if (-not $native) {
            wsl -e sh -c 'command -v shellcheck' *> $null
            $wslHas = ($LASTEXITCODE -eq 0)
        }

        Write-Host "== shellcheck ($($files.Count) files)" -ForegroundColor Cyan
        if ($native) {
            & $native.Source @shArgs @files
        } elseif ($wslHas) {
            wsl -e shellcheck @shArgs @files
        } elseif (Get-Command docker -ErrorAction SilentlyContinue) {
            Write-Host "  (via koalaman/shellcheck container)" -ForegroundColor DarkGray
            docker run --rm -v "${repo}:/mnt" -w /mnt koalaman/shellcheck:stable @shArgs @files
        } else {
            Write-Warning "no shellcheck available (install it, add it to WSL, or start Docker) - SKIPPED"
            $LASTEXITCODE = 0
        }
        if ($LASTEXITCODE -ne 0) { $failed += 'shellcheck' } else { Write-Host "  ok" -ForegroundColor Green }
    }

    # --- command reference ------------------------------------------------
    # Generated from the binary's own --help, so editing a flag's help text
    # without regenerating would ship a reference describing the last release.
    Write-Host "== command reference" -ForegroundColor Cyan
    & pwsh -File (Join-Path $PSScriptRoot 'build-reference.ps1') -Check
    if ($LASTEXITCODE -ne 0) { $failed += 'reference' }

    # --- markdown links ---------------------------------------------------
    Write-Host "== links" -ForegroundColor Cyan
    & pwsh -File (Join-Path $PSScriptRoot 'check-links.ps1')
    if ($LASTEXITCODE -ne 0) { $failed += 'links' }

    # --- go ---------------------------------------------------------------
    if (-not $SkipGo) {
        $go = Get-Command go -ErrorAction SilentlyContinue
        if (-not $go) {
            # A freshly installed Go is usually missing from an already-open shell.
            $cands = @("$env:ProgramFiles\Go\bin\go.exe",
                       "$env:LOCALAPPDATA\Programs\Go\bin\go.exe")
            foreach ($c in $cands) {
                if (Test-Path $c) {
                    $env:PATH = "$(Split-Path $c);$env:PATH"
                    $go = Get-Command $c
                    break
                }
            }
        }
        if ($go) {
            Write-Host "== gofmt / vet / test" -ForegroundColor Cyan
            $bad = gofmt -l .
            if ($bad) { Write-Host "gofmt needed:"; $bad; $failed += 'gofmt' }
            go vet ./...   ; if ($LASTEXITCODE -ne 0) { $failed += 'go vet' }
            go build ./... ; if ($LASTEXITCODE -ne 0) { $failed += 'go build' }
            go test ./...  ; if ($LASTEXITCODE -ne 0) { $failed += 'go test' }
            if ($failed.Count -eq 0) { Write-Host "  ok" -ForegroundColor Green }
        } else {
            Write-Warning "go not installed - Go checks SKIPPED (winget install GoLang.Go)"
        }
    }

    if ($failed.Count) {
        Write-Host "`nFAILED: $($failed -join ', ')" -ForegroundColor Red
        exit 1
    }
    Write-Host "`nAll local checks passed." -ForegroundColor Green
} finally {
    Pop-Location
}
