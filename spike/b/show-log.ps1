# Render the probe log readably. Optional -Mode filters to console or service runs.
param([string]$Mode)

$log = 'C:\ProgramData\skrog-spike-b\probe.log'
if (-not (Test-Path $log)) { Write-Host "no log yet at $log" -ForegroundColor Yellow; return }

Get-Content $log | ForEach-Object {
    try { $e = $_ | ConvertFrom-Json } catch { return }
    if ($Mode -and $e.mode -ne $Mode) { return }

    $flag = if ($e.ok -eq $true) { 'OK  ' } elseif ($e.ok -eq $false) { 'FAIL' } else { '    ' }
    $color = if ($e.ok -eq $true) { 'Green' } elseif ($e.ok -eq $false) { 'Red' } else { 'Gray' }
    Write-Host ("{0}  {1,-8} {2,-16} {3}" -f `
        ([datetime]::Parse($e.time).ToString('HH:mm:ss')), $e.mode, $e.phase, $flag) -NoNewline -ForegroundColor $color
    $extra = @($e.user, $e.detail) | Where-Object { $_ }
    if ($e.session -ne $null -and $e.phase -eq 'identity') { $extra += "session=$($e.session)" }
    Write-Host (" " + ($extra -join '  '))
    if ($e.error) { Write-Host "      error: $($e.error)" -ForegroundColor Red }
    if ($e.output) {
        ($e.output -split "`n" | Select-Object -First 6) | ForEach-Object {
            Write-Host "      | $($_.TrimEnd())" -ForegroundColor DarkGray
        }
    }
}
