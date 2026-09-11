# Spike A step 5: automated checks against the relay. relay.exe must be running.
$ErrorActionPreference = 'Stop'
$env:DOCKER_HOST = 'npipe:////./pipe/skrog_spike'

Write-Host "== docker version =="
docker version
if ($LASTEXITCODE -ne 0) { throw "docker version failed - is relay.exe running?" }

Write-Host "`n== hello-world =="
docker run --rm hello-world
if ($LASTEXITCODE -ne 0) { throw "hello-world failed" }

Write-Host "`n== latency: 10x docker version =="
$times = 1..10 | ForEach-Object { (Measure-Command { docker version *> $null }).TotalMilliseconds }
$m = $times | Measure-Object -Average -Minimum -Maximum
Write-Host ("avg {0:N0} ms  min {1:N0} ms  max {2:N0} ms   (record in issue #2)" -f $m.Average, $m.Minimum, $m.Maximum)

Write-Host "`n== binary safety: docker save x2, hashes must match =="
docker pull alpine:latest | Out-Null
$t1 = Join-Path $env:TEMP 'spike-save-1.tar'; $t2 = Join-Path $env:TEMP 'spike-save-2.tar'
docker save alpine:latest -o $t1
docker save alpine:latest -o $t2
$h1 = (Get-FileHash $t1 -Algorithm SHA256).Hash; $h2 = (Get-FileHash $t2 -Algorithm SHA256).Hash
Remove-Item $t1, $t2 -Force
if ($h1 -ne $h2) { throw "docker save not deterministic across the relay: $h1 vs $h2" }
Write-Host "identical: $h1"

Write-Host "`nAutomated checks PASSED. Now the manual list in README.md (exec -it, logs -f, build, coexistence)."
