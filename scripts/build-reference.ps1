# Generate docs/reference.md from the binary's own --help output (#209).
#
# The help text is the best-maintained description of every flag, because it is
# the one a user is shown when they get it wrong. A reference written by hand
# beside it is a second copy that drifts, and the drift is invisible until
# someone follows it.
#
# So this asks the binary. Every command in `hawser help --json` is run with
# --help and its output captured verbatim; nothing here knows what any command
# does, which is the property that keeps it correct.
#
#   pwsh -File scripts/build-reference.ps1          # regenerate
#   pwsh -File scripts/build-reference.ps1 -Check   # fail if it would change
#
# -Check is what CI runs: editing a flag's help without regenerating should
# fail in review, not ship a reference that describes the previous release.
[CmdletBinding()]
param(
    [switch]$Check,
    # Use an existing binary instead of building one. The generated text must
    # come from the working tree, so the default is to build.
    [string]$Exe
)

$ErrorActionPreference = 'Stop'
$repo = Split-Path $PSScriptRoot -Parent
$out = Join-Path $repo 'docs/reference.md'

Push-Location $repo
try {
    if (-not $Exe) {
        $Exe = Join-Path ([IO.Path]::GetTempPath()) 'hawser-reference.exe'
        Write-Host "building hawser..." -ForegroundColor DarkGray
        # No version stamp: the reference must not change just because it was
        # generated from a tagged build rather than a working tree.
        & go build -o $Exe ./cmd/hawser
        if ($LASTEXITCODE -ne 0) { throw "go build failed" }
    }

    $index = & $Exe help --json | ConvertFrom-Json
    if (-not $index) { throw "``hawser help --json`` returned nothing" }

    # `--help` prints usage on stderr and exits 2 (the usage code), because for
    # every other invocation that text accompanies a mistake. Both are expected
    # here, so stderr is merged in and the exit code is not treated as failure.
    function Get-Help([string]$command) {
        $text = & $Exe $command --help 2>&1 | Out-String
        return $text.Replace("`r`n", "`n").TrimEnd()
    }

    $sb = [System.Text.StringBuilder]::new()
    $null = $sb.AppendLine(@'
# Command reference

Every command, its flags and its exit codes — generated from the binary's own
`--help` output by `scripts/build-reference.ps1`, so it cannot drift from what
the CLI actually does. CI regenerates it and fails if the result differs.

Run `hawser <command> --help` for the same text in your terminal.

'@)

    $null = $sb.AppendLine('## Commands')
    $null = $sb.AppendLine()
    foreach ($c in $index) {
        # The summary is markdown (it contains backticks); the anchor is the
        # command name, which is always a safe slug.
        $null = $sb.AppendLine("- [``$($c.name)``](#$($c.name)) — $($c.summary)")
    }
    $null = $sb.AppendLine()

    $null = $sb.AppendLine(@'
## Exit codes

Shared by every command, and part of the contract CI scripts branch on:

| code | meaning |
| --- | --- |
| 0 | success |
| 1 | error |
| 2 | usage — the command was called wrongly |
| 3 | asked about something that is not installed |

Individual commands add their own where they need a third answer; each one
says so in its help text below.

'@)

    foreach ($c in $index) {
        $null = $sb.AppendLine("## $($c.name)")
        $null = $sb.AppendLine()
        $null = $sb.AppendLine($c.summary)
        $null = $sb.AppendLine()
        $null = $sb.AppendLine('```')
        $null = $sb.AppendLine((Get-Help $c.name))
        $null = $sb.AppendLine('```')
        $null = $sb.AppendLine()
    }

    # Normalized to LF and written whole, so the file is identical whatever a
    # contributor's autocrlf setting is - otherwise -Check fails on Windows for
    # reasons that have nothing to do with the help text.
    $text = $sb.ToString().Replace("`r`n", "`n").TrimEnd() + "`n"

    if ($Check) {
        if (-not (Test-Path $out)) {
            Write-Host "docs/reference.md does not exist; run scripts/build-reference.ps1" -ForegroundColor Red
            exit 1
        }
        $current = [IO.File]::ReadAllText($out).Replace("`r`n", "`n")
        if ($current -ne $text) {
            Write-Host "docs/reference.md is out of date with the binary's --help output." -ForegroundColor Red
            Write-Host "Run: pwsh -File scripts/build-reference.ps1" -ForegroundColor Red
            exit 1
        }
        Write-Host "docs/reference.md matches the binary" -ForegroundColor Green
        exit 0
    }

    [IO.File]::WriteAllText($out, $text)
    Write-Host "wrote docs/reference.md ($($index.Count) commands)" -ForegroundColor Green
} finally {
    Pop-Location
}
