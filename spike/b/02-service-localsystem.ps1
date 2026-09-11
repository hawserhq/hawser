# Spike B step 2: run the probe as a service under LocalSystem.
#
# The cheapest thing that could work, and the most likely to fail: LocalSystem
# lives in session 0 and has its own registry hive, so the distro the
# interactive user imported may be invisible. Whichever way it goes, the log
# says why.
$ErrorActionPreference = 'Stop'
$here = $PSScriptRoot
$svc = 'SkrogSpikeB'
$exe = Join-Path $here 'agent\probe.exe'

if (-not (Test-Path $exe)) { throw "run 01-preflight.ps1 first (probe.exe missing)" }

if (Get-Service $svc -ErrorAction SilentlyContinue) {
    Write-Host "removing previous $svc service" -ForegroundColor Yellow
    sc.exe stop $svc | Out-Null
    Start-Sleep -Seconds 2
    sc.exe delete $svc | Out-Null
    Start-Sleep -Seconds 1
}

Write-Host "== creating $svc under LocalSystem ==" -ForegroundColor Cyan
# SKROG_SPIKE_DISTRO cannot be passed as a service env var without a wrapper,
# so the probe's built-in default (skrog-spike-b) is what it will use.
sc.exe create $svc binPath= "`"$exe`"" start= demand obj= "LocalSystem" DisplayName= "Skrog Spike B (LocalSystem)"
if ($LASTEXITCODE -ne 0) { throw "sc create failed" }

Write-Host "`n== starting ==" -ForegroundColor Cyan
sc.exe start $svc
Start-Sleep -Seconds 20

Write-Host "`n== service state ==" -ForegroundColor Cyan
sc.exe query $svc | Select-String "STATE|WIN32_EXIT_CODE"

Write-Host "`n== probe log ==" -ForegroundColor Cyan
& (Join-Path $here 'show-log.ps1') -Mode service

Write-Host @"

Read the log above. The decisive line is 'distro-visible':

  ok=true   LocalSystem sees the distro -> session 0 works, this is the simplest
            viable pattern. Continue to 04 to test the no-login window.
  ok=false  per-user registration is the blocker (expected). The distro must be
            imported as whichever account the service runs under - that is what
            03 tests with a dedicated account.

Next: .\03-service-dedicated-account.ps1
"@ -ForegroundColor Green
