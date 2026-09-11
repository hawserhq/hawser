# Spike B step 5: evaluate the no-login window and print the verdict.
$ErrorActionPreference = 'Stop'
$log = 'C:\ProgramData\skrog-spike-b\probe.log'
$marker = 'C:\ProgramData\skrog-spike-b\nologin-start.txt'

if (-not (Test-Path $log)) { throw "no probe log at $log" }

$events = Get-Content $log | ForEach-Object { try { $_ | ConvertFrom-Json } catch {} }

Write-Host "== identity the service ran as ==" -ForegroundColor Cyan
$events | Where-Object { $_.phase -eq 'identity' -and $_.mode -eq 'service' } |
    Select-Object -Last 3 | ForEach-Object { "  $($_.user)  session=$($_.session)" }

Write-Host "`n== decisive checks (service mode) ==" -ForegroundColor Cyan
foreach ($p in 'wsl-status', 'wsl-list', 'distro-visible', 'distro-exec', 'socket-up') {
    $e = $events | Where-Object { $_.phase -eq $p -and $_.mode -eq 'service' } | Select-Object -Last 1
    if ($null -eq $e) { Write-Host ("  {0,-16} (not reached)" -f $p) -ForegroundColor DarkGray; continue }
    $ok = if ($e.ok -eq $true) { 'OK  ' } elseif ($e.ok -eq $false) { 'FAIL' } else { '--  ' }
    $color = if ($e.ok -eq $true) { 'Green' } elseif ($e.ok -eq $false) { 'Red' } else { 'Gray' }
    Write-Host ("  {0,-16} {1} {2}" -f $p, $ok, $e.detail) -ForegroundColor $color
    if ($e.error) { Write-Host "        error: $($e.error)" -ForegroundColor Red }
}

if (Test-Path $marker) {
    $start = [datetime]::Parse((Get-Content $marker))
    Write-Host "`n== no-login window (began $($start.ToString('u'))) ==" -ForegroundColor Cyan

    $beats = $events | Where-Object {
        $_.phase -eq 'heartbeat' -and $_.time -and ([datetime]::Parse($_.time)) -gt $start
    }
    if (-not $beats) {
        Write-Host "  no heartbeats after the marker - the service was not running during the window" -ForegroundColor Red
    } else {
        $good = ($beats | Where-Object { $_.ok -eq $true }).Count
        $bad = ($beats | Where-Object { $_.ok -ne $true }).Count
        $span = ([datetime]::Parse(($beats | Select-Object -Last 1).time)) - $start
        Write-Host "  heartbeats: $good healthy, $bad unhealthy, spanning $([int]$span.TotalMinutes) min"
        if ($bad -eq 0 -and $good -ge 4) {
            Write-Host "  engine stayed reachable with no user logged in" -ForegroundColor Green
        } elseif ($good -gt 0) {
            Write-Host "  intermittent - inspect the log directly" -ForegroundColor Yellow
        } else {
            Write-Host "  engine was NOT reachable during the window" -ForegroundColor Red
        }
    }
} else {
    Write-Host "`n(no-login window not started - run 04-nologin-window.ps1)" -ForegroundColor DarkGray
}

Write-Host @"

== Verdict for issue #3 ==

GO   if distro-visible, distro-exec and socket-up are OK in service mode AND
     the no-login window shows healthy heartbeats. Record which pattern worked
     (LocalSystem from 02, or dedicated account from 03) - that decides what
     `skrog install --headless` has to do in v0.2.

NO-GO / PARTIAL if the no-login window fails. That does not kill the project:
     it demotes "no user logged in" from a headline claim to "requires auto-logon",
     and PLAN section 06 gets rewritten honestly before v0.1 publishes.

Paste this output into issue #3, then run .\99-cleanup.ps1
"@ -ForegroundColor Cyan
