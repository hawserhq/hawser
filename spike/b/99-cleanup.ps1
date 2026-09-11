# Spike B cleanup: remove the service, task, account, distros, and logs.
$ErrorActionPreference = 'Continue'
$here = $PSScriptRoot

Write-Host "== service ==" -ForegroundColor Cyan
sc.exe stop SkrogSpikeB 2>$null | Out-Null
Start-Sleep -Seconds 2
sc.exe delete SkrogSpikeB 2>$null | Out-Null

Write-Host "== scheduled task ==" -ForegroundColor Cyan
schtasks /Delete /TN SkrogSpikeBImport /F 2>$null | Out-Null
Unregister-ScheduledTask -TaskName SkrogSpikeBTask -Confirm:$false -ErrorAction SilentlyContinue

Write-Host "== distros ==" -ForegroundColor Cyan
foreach ($d in 'skrog-spike-b', 'skrog-spike-b-svc', 'skrog-spike-b-task') {
    wsl --terminate $d 2>$null | Out-Null
    wsl --unregister $d 2>$null | Out-Null
}
# The service account owns its own registration, which the interactive user
# cannot unregister; removing the account and its profile takes it with them.

Write-Host "== local account ==" -ForegroundColor Cyan
if (Get-LocalUser skrog-svc -ErrorAction SilentlyContinue) {
    Remove-LocalUser -Name skrog-svc
    Write-Host "  removed skrog-svc" -ForegroundColor Green
}
# Its profile directory, if a logon ever created one.
Get-CimInstance Win32_UserProfile -ErrorAction SilentlyContinue |
    Where-Object { $_.LocalPath -like '*skrog-svc*' } |
    # Remove-LocalUser leaves the directory behind when the profile came from a
    # service logon, so the directory is removed explicitly below too.
    ForEach-Object { Remove-CimInstance $_ -ErrorAction SilentlyContinue }
Remove-Item -Recurse -Force 'C:\Users\skrog-svc' -ErrorAction SilentlyContinue

Write-Host "== files ==" -ForegroundColor Cyan
Remove-Item -Recurse -Force 'C:\ProgramData\skrog-spike-b' -ErrorAction SilentlyContinue
Remove-Item -Recurse -Force (Join-Path $env:LOCALAPPDATA 'skrog-spike-b') -ErrorAction SilentlyContinue
Remove-Item -Force (Join-Path $here 'agent\probe.exe') -ErrorAction SilentlyContinue
Get-ChildItem $env:TEMP -Filter 'skrog-spike-import-*.ps1' -ErrorAction SilentlyContinue | Remove-Item -Force

Write-Host "`nSpike B cleanup done. Verify nothing is left:" -ForegroundColor Green
Write-Host "  distros:" -ForegroundColor DarkGray
(wsl --list --quiet) -replace "`0","" | Where-Object { $_ -match '\S' } | ForEach-Object { "    $_" }
Write-Host "  SeServiceLogonRight may still list a stale SID; harmless once the account is gone." -ForegroundColor DarkGray
