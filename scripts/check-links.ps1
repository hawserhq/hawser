# Verify every relative link in the repo's Markdown resolves to a real file.
#
# The README is the only index there is until a docs site exists (#197), and an
# index is worth exactly as much as its worst link. This catches the two ways
# that rots: a page gets renamed and its links are not, or a new page is added
# and never linked at all.
#
#   pwsh -File scripts/check-links.ps1
#
# Exit 0 when every link resolves, 1 otherwise.
[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
$repo = Split-Path $PSScriptRoot -Parent
Push-Location $repo
try {
    $md = Get-ChildItem -Recurse -Filter *.md -File |
        Where-Object { $_.FullName -notmatch '\\(node_modules|\.git)\\' }

    $broken = @()
    $linked = [System.Collections.Generic.HashSet[string]]::new()

    foreach ($file in $md) {
        $text = Get-Content -Raw $file.FullName
        # Markdown inline links and reference definitions alike: capture the
        # target of ](...) and of "[label]: target".
        $targets = @()
        $targets += [regex]::Matches($text, '\]\(([^)\s]+)') | ForEach-Object { $_.Groups[1].Value }
        $targets += [regex]::Matches($text, '(?m)^\[[^\]]+\]:\s*(\S+)') | ForEach-Object { $_.Groups[1].Value }

        foreach ($t in $targets) {
            # Not our problem: absolute URLs, mailto, and same-page anchors.
            if ($t -match '^(https?:|mailto:|#)') { continue }

            # Strip any anchor; we check that the file exists, not that the
            # heading does — heading drift is noise, a missing file is a bug.
            $path = ($t -split '#')[0]
            if (-not $path) { continue }

            $resolved = Join-Path (Split-Path $file.FullName) $path
            if (Test-Path $resolved) {
                $full = (Resolve-Path $resolved).Path
                if ($full -like '*.md') { [void]$linked.Add($full) }
            } else {
                $rel = [IO.Path]::GetRelativePath($repo, $file.FullName).Replace('\', '/')
                $broken += "  $rel -> $t"
            }
        }
    }

    if ($broken) {
        Write-Host "broken links:" -ForegroundColor Red
        $broken | ForEach-Object { Write-Host $_ }
        exit 1
    }

    # Unlinked pages are not an error -- a page can be reached from another
    # page rather than the README -- but they are worth naming, since an
    # unreachable page is a page nobody reads.
    $orphans = $md |
        Where-Object { -not $linked.Contains($_.FullName) } |
        Where-Object { $_.Name -notin @('README.md', 'PLAN.md', 'ROADMAP.md', 'RELEASING.md', 'CHANGELOG.md') } |
        ForEach-Object { [IO.Path]::GetRelativePath($repo, $_.FullName).Replace('\', '/') }

    Write-Host "links: all resolve ($($md.Count) files)" -ForegroundColor Green
    if ($orphans) {
        Write-Host "note: not linked from any page:" -ForegroundColor Yellow
        $orphans | ForEach-Object { Write-Host "  $_" }
    }
    exit 0
} finally {
    Pop-Location
}
