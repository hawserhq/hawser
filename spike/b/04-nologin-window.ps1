# Spike B step 4: the check that actually decides the gate.
#
# Everything before this ran while a user was logged in. The claim under test is
# "works with no user logged in", and the only honest way to test it is to log
# off, leave the service running, and read the heartbeat afterwards.
$ErrorActionPreference = 'Stop'
$here = $PSScriptRoot
$svc = 'SkrogSpikeB'

$s = Get-Service $svc -ErrorAction SilentlyContinue
if (-not $s) { throw "no $svc service - run 02 or 03 first" }
if ($s.Status -ne 'Running') { throw "$svc is $($s.Status); start it before logging off" }

# Survive a reboot too, which is the CI-runner case: the machine comes up and
# nobody logs in at all.
sc.exe config $svc start= auto | Out-Null
Write-Host "$svc set to automatic start (survives reboot)" -ForegroundColor Green

$marker = 'C:\ProgramData\skrog-spike-b\nologin-start.txt'
(Get-Date).ToString('o') | Set-Content $marker

Write-Host @"

== Now do this by hand ==

  1. SIGN OUT completely (Start > user > Sign out).
     Do NOT just lock the screen - locking keeps your session alive and proves
     nothing. RDP disconnect does not count either.
  2. Leave the machine alone for at least 3 minutes.
  3. Sign back in and run:  .\05-verdict.ps1

Optional, and the stronger test for the CI-runner claim:
  1. Reboot instead of signing out.
  2. At the login screen, wait 3 minutes WITHOUT logging in.
  3. Log in and run .\05-verdict.ps1

The service writes a heartbeat every 15s, so the log will show whether the
engine stayed reachable while nobody was present. Recorded start of window:
  $(Get-Content $marker)
"@ -ForegroundColor Cyan
