# Spike A step 4: start dockerd hidden, wait for its socket.
$ErrorActionPreference = 'Stop'

Write-Host "Starting dockerd in skrog-spike ..."
Start-Process -WindowStyle Hidden wsl.exe -ArgumentList '-d','skrog-spike','-u','root','--','sh','-c','dockerd >/var/log/dockerd.log 2>&1'

$deadline = (Get-Date).AddSeconds(30)
do {
    Start-Sleep -Milliseconds 500
    wsl -d skrog-spike -u root --exec test -S /var/run/docker.sock
    if ($LASTEXITCODE -eq 0) {
        Write-Host "docker.sock is up."
        Write-Host "Next: cd relay; go mod tidy; go build; .\relay.exe"
        exit 0
    }
} while ((Get-Date) -lt $deadline)

Write-Host "Socket never appeared. Log tail:"
wsl -d skrog-spike -u root --exec tail -50 /var/log/dockerd.log
exit 1
