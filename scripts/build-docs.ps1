# Stage docs/ into site/content/ and build the documentation site (#197).
#
# docs/ stays the source of truth. It is plain markdown that renders on GitHub,
# reviews as a diff, and is what `hawser doctor` and every error message point
# at. The site is a *view* of it, so the only thing this script adds is the
# front matter Hugo needs and the grouping site/data/nav.yaml already records.
#
#   pwsh -File scripts/build-docs.ps1            # stage + build to site/public
#   pwsh -File scripts/build-docs.ps1 -Serve     # stage + live preview
#   pwsh -File scripts/build-docs.ps1 -StageOnly # just write site/content
#   pwsh -File scripts/build-docs.ps1 -Strict    # what CI runs
#
# site/content is generated and git-ignored. Never edit it; edit docs/.
[CmdletBinding()]
param(
    [switch]$Serve,
    [switch]$StageOnly,
    # Turn "this page is in docs/ but in no nav section" from a warning into a
    # failure. CI passes it: a page nothing links to is a page nobody reads,
    # and a pull request is where that is cheap to fix.
    [switch]$Strict,
    [string]$BaseURL
)

$ErrorActionPreference = 'Stop'
$repo = Split-Path $PSScriptRoot -Parent
$docs = Join-Path $repo 'docs'
$site = Join-Path $repo 'site'
$content = Join-Path $site 'content'

# Links that leave docs/ cannot resolve inside the site; send them to the file
# on GitHub, which is where a reader of RELEASING.md wants to end up anyway.
$blob = 'https://github.com/hawserhq/hawser/blob/main'

function Read-Nav {
    # A deliberately small reader for the one shape site/data/nav.yaml uses.
    # Taking a dependency on a YAML module to read a 30-line file we also
    # wrote would be the more fragile choice, not the less.
    $navPath = Join-Path $site 'data/nav.yaml'
    $order = [ordered]@{}
    $short = [ordered]@{}
    $section = $null
    $inShort = $false
    $i = 0
    foreach ($line in Get-Content $navPath) {
        if (-not $line.Trim() -or $line -match '^\s*#') { continue }
        if ($line -match '^short:') { $inShort = $true; continue }
        if ($line -match '^sections:') { $inShort = $false; continue }
        if ($inShort) {
            if ($line -match '^\s+([A-Za-z0-9_-]+):\s*(.+?)\s*$') {
                $short[$Matches[1]] = $Matches[2].Trim('"').Trim("'")
            }
            continue
        }
        if ($line -match '^\s*-\s+section:\s*(.+?)\s*$') {
            $section = $Matches[1].Trim('"').Trim("'")
            continue
        }
        if ($line -match '^\s*pages:\s*\[(.+)\]\s*$') {
            foreach ($slug in ($Matches[1] -split ',')) {
                $slug = $slug.Trim()
                if (-not $slug) { continue }
                $i += 10
                $order[$slug] = [pscustomobject]@{ Section = $section; Weight = $i; Short = '' }
            }
        }
    }
    if ($order.Count -eq 0) { throw "nav.yaml parsed to nothing - has its shape changed?" }
    foreach ($k in $short.Keys) {
        if (-not $order.Contains($k)) { throw "nav.yaml: short label for '$k', which is in no section" }
        $order[$k].Short = $short[$k]
    }
    return $order
}

# Turn the opening prose of a page into a one-line summary for the index cards
# and the meta description. First sentence only: anything longer is a paragraph
# pretending to be a summary.
function Get-Summary([string[]]$bodyLines) {
    $para = @()
    foreach ($l in $bodyLines) {
        $t = $l.Trim()
        if (-not $t) { if ($para.Count) { break } else { continue } }
        # Skip anything that is not prose: badges, quotes, code, headings, lists.
        if ($t -match '^(#|>|```|\||[-*+]\s|\d+\.\s|<)') { if ($para.Count) { break } else { continue } }
        $para += $t
    }
    if (-not $para.Count) { return '' }
    $text = ($para -join ' ')
    # Strip link syntax, emphasis and inline code: a meta description is plain
    # text, and a stray backtick in a search result looks like a typo.
    $text = $text -replace '\[([^\]]+)\]\([^)]*\)', '$1'
    $text = $text -replace '[`*_]', ''
    $text = $text -replace '\s+', ' '
    # A bare "(corporate-network.md)" is what is left once a link whose label
    # was its own filename has been unwrapped. Useful in the page, noise here.
    $text = $text -replace '\s*\([^)]*\.md\)', ''
    # First sentence: a period followed by a space and a capital, so "v1.45"
    # and "Docker, Inc." do not end it early.
    if ($text -match '^(.{20,}?[.!?])\s+[A-Z(`]') { $text = $Matches[1] }
    if ($text.Length -gt 180) { $text = $text.Substring(0, 177).TrimEnd() + '...' }
    return $text.Trim()
}

function ConvertTo-YamlString([string]$s) {
    # The escape character is built from a char code so that escaping it is not
    # itself something to get wrong.
    $bs = [string][char]92
    '"' + $s.Replace($bs, $bs + $bs).Replace('"', $bs + '"') + '"'
}

$nav = Read-Nav
$pages = Get-ChildItem $docs -Filter *.md -File | Sort-Object Name

if (Test-Path $content) { Remove-Item $content -Recurse -Force }
New-Item -ItemType Directory -Path $content | Out-Null

$unlisted = @()
foreach ($page in $pages) {
    $slug = [IO.Path]::GetFileNameWithoutExtension($page.Name)
    $lines = Get-Content $page.FullName

    # The H1 is the page title; the layout renders it, so drop it from the body
    # rather than shipping two of them.
    $titleIdx = -1
    for ($i = 0; $i -lt $lines.Count; $i++) {
        if ($lines[$i] -match '^#\s+(.+?)\s*$') { $titleIdx = $i; break }
    }
    if ($titleIdx -lt 0) { throw "$($page.Name) has no '# Title' heading" }
    $title = ($lines[$titleIdx] -replace '^#\s+', '').Trim()
    $body = if ($titleIdx + 1 -lt $lines.Count) { $lines[($titleIdx + 1)..($lines.Count - 1)] } else { @() }

    $meta = $nav[$slug]
    if (-not $meta) {
        $unlisted += $page.Name
        $meta = [pscustomobject]@{ Section = 'Other'; Weight = 9000; Short = '' }
    }

    $text = ($body -join "`n").TrimStart("`n")
    # ](../FILE.md) and friends leave the docs tree entirely.
    $text = [regex]::Replace($text, '\]\(\.\./([^)\s]+)\)',
        { param($m) '](' + $blob + '/' + $m.Groups[1].Value + ')' })

    $fm = @('---', "title: $(ConvertTo-YamlString $title)")
    # linkTitle only where nav.yaml overrides it; Hugo falls back to the title.
    if ($meta.Short) { $fm += "linkTitle: $(ConvertTo-YamlString $meta.Short)" }
    $fm += @(
        "description: $(ConvertTo-YamlString (Get-Summary $body))",
        "section: $(ConvertTo-YamlString $meta.Section)",
        "weight: $($meta.Weight)",
        "source: $($page.Name)",
        "# Generated by scripts/build-docs.ps1 from docs/$($page.Name) -- do not edit.",
        '---',
        ''
    )

    # WriteAllText rather than Set-Content: the staged file is exactly what we
    # composed, on any Git autocrlf setting.
    [IO.File]::WriteAllText((Join-Path $content "$slug.md"), (($fm -join "`n") + $text + "`n"))
}

# The home page intro is prose, not generated: it lives in site/home.md and is
# the one page here with no docs/ counterpart.
Copy-Item (Join-Path $site 'home.md') (Join-Path $content '_index.md')

Write-Host "staged $($pages.Count) pages into site/content" -ForegroundColor Green

if ($unlisted.Count) {
    $msg = "not listed in site/data/nav.yaml, so nothing links to them: $($unlisted -join ', ')"
    if ($Strict) {
        Write-Host $msg -ForegroundColor Red
        exit 1
    }
    Write-Warning $msg
}

# nav.yaml pointing at a page that no longer exists is the other direction of
# the same rot, and Hugo only warns about it. Fail here instead.
$missing = @($nav.Keys | Where-Object { -not (Test-Path (Join-Path $docs "$_.md")) })
if ($missing.Count) {
    Write-Host "site/data/nav.yaml lists pages that do not exist: $($missing -join ', ')" -ForegroundColor Red
    exit 1
}

if ($StageOnly) { return }

$hugo = Get-Command hugo -ErrorAction SilentlyContinue
if (-not $hugo) {
    # Where a portable install lands when it is not on PATH.
    $local = Join-Path $env:LOCALAPPDATA 'Programs/hugo/hugo.exe'
    if (Test-Path $local) { $hugo = Get-Command $local }
}
if (-not $hugo) {
    Write-Host "hugo not found. Install it with" -ForegroundColor Red
    Write-Host "  winget install Hugo.Hugo" -ForegroundColor Red
    Write-Host "or unpack a release from https://github.com/gohugoio/hugo/releases" -ForegroundColor Red
    exit 1
}

Push-Location $site
try {
    if ($Serve) {
        & $hugo.Source server --disableFastRender --printPathWarnings
    } else {
        $hugoArgs = @('--gc', '--minify', '--cleanDestinationDir', '--printPathWarnings')
        # Pages passes its own; locally the config default applies.
        if ($BaseURL) { $hugoArgs += @('--baseURL', $BaseURL) }
        & $hugo.Source @hugoArgs
    }
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
} finally {
    Pop-Location
}
