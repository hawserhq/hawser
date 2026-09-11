# Spike B step 3: dedicated service account that provisions its own distro.
#
# The pattern PLAN §09 proposes, and after step 02 the only one left: WSL refuses
# to run as LocalSystem outright (WSL_E_LOCAL_SYSTEM_NOT_SUPPORTED), so a real
# account is the only remaining option.
#
# The service imports the distro itself rather than having something else import
# it beforehand. Two reasons: WSL registration is per-user, so the account that
# runs the service must be the account that owns the distro; and running
# `wsl --import` as another account from outside needs stored-credential rights
# a bare service account does not have (schtasks fails with "A specified logon
# session does not exist"). Self-import is also what `skrog install --headless`
# would have to do in production, so it is the more useful thing to measure.
$ErrorActionPreference = 'Stop'
$here = $PSScriptRoot
$svc = 'SkrogSpikeB'
$user = 'skrog-svc'
$distro = 'skrog-spike-b-svc'
$exe = Join-Path $here 'agent\probe.exe'
$dataRoot = 'C:\ProgramData\skrog-spike-b'

if (-not (Test-Path $exe)) { throw "run 01-preflight.ps1 first (probe.exe missing)" }

function Assert-Admin {
    $id = [Security.Principal.WindowsIdentity]::GetCurrent()
    if (-not ([Security.Principal.WindowsPrincipal]::new($id)).IsInRole(
        [Security.Principal.WindowsBuiltInRole]::Administrator)) {
        throw "run this from an elevated PowerShell"
    }
}
Assert-Admin

# Generated locally, used only for these API calls, never printed or written to
# disk. A real installer would use a virtual service account or a managed one.
function New-RandomPassword {
    param([int]$Length = 24)
    # Not System.Web.Security.Membership: that is .NET Framework only, so it is
    # absent under PowerShell 7 (.NET Core). RandomNumberGenerator::Create()
    # exists on both, so this works in 5.1 and 7 alike.
    #
    # Symbols are restricted to ones that survive sc.exe argument parsing
    # unscathed - no quotes, ampersands, carets or percent signs.
    $sets = @(
        'ABCDEFGHIJKLMNPQRSTUVWXYZ',
        'abcdefghijkmnopqrstuvwxyz',
        '23456789',
        '!#$*+-=?@_'
    )
    $rng = [System.Security.Cryptography.RandomNumberGenerator]::Create()
    try {
        $bytes = [byte[]]::new(4)
        $pick = {
            param($chars)
            $rng.GetBytes($bytes)
            $n = [BitConverter]::ToUInt32($bytes, 0)
            $chars[[int]($n % [uint32]$chars.Length)]
        }
        # One character from each set first, so Windows password-complexity
        # policy is satisfied regardless of what the rest happens to draw.
        $chars = foreach ($s in $sets) { & $pick $s }
        $all = -join $sets
        $chars += 1..($Length - $sets.Count) | ForEach-Object { & $pick $all }
        -join ($chars | Sort-Object { & $pick '0123456789' })
    } finally {
        $rng.Dispose()
    }
}

function Grant-UserRight {
    param([string]$Account, [string]$Right)
    # No PowerShell cmdlet exposes user rights, so secedit is the built-in path.
    $tmp = Join-Path $env:TEMP "secpol-$PID-$Right"
    New-Item -ItemType Directory -Force $tmp | Out-Null
    try {
        $inf = Join-Path $tmp 'export.inf'
        $db = Join-Path $tmp 'secedit.sdb'
        secedit /export /cfg $inf /quiet

        $sid = (Get-LocalUser $Account).SID.Value
        $content = Get-Content $inf
        $line = $content | Where-Object { $_ -match "^$Right\s*=" }
        if ($line) {
            if ($line -match [regex]::Escape($sid)) {
                Write-Host "  $Right already granted" -ForegroundColor DarkGray
                return
            }
            $content = $content -replace [regex]::Escape($line), "$line,*$sid"
        } else {
            $content = $content -replace '(\[Privilege Rights\])', "`$1`r`n$Right = *$sid"
        }
        $applied = Join-Path $tmp 'apply.inf'
        $content | Set-Content $applied -Encoding Unicode
        secedit /configure /db $db /cfg $applied /areas USER_RIGHTS /quiet
        Write-Host "  granted $Right" -ForegroundColor Green
    } finally {
        Remove-Item $tmp -Recurse -Force -ErrorAction SilentlyContinue
    }
}

$pw = New-RandomPassword -Length 24
$sec = ConvertTo-SecureString $pw -AsPlainText -Force

if (-not (Get-LocalUser $user -ErrorAction SilentlyContinue)) {
    Write-Host "== creating local account $user ==" -ForegroundColor Cyan
    New-LocalUser -Name $user -Password $sec -PasswordNeverExpires `
        -AccountNeverExpires -Description "Skrog Spike B service account" | Out-Null
    # Deliberately not added to Administrators: whether least privilege
    # suffices is part of what is being measured.
} else {
    Write-Host "== resetting password for existing $user ==" -ForegroundColor Yellow
    Set-LocalUser -Name $user -Password $sec
}

Write-Host "== granting user rights ==" -ForegroundColor Cyan
# SeServiceLogonRight is the obvious one. The other two are not:
#
# Creating the WSL utility VM goes through the Host Compute Service, which
# failed with Wsl/Service/RegisterDistro/CreateVm/HCS/0x80070569 -
# ERROR_LOGON_TYPE_NOT_GRANTED - when the account held only the service right.
# HCS constructs the VM against a user token that needs batch (and on some
# builds interactive) logon rights, even though nobody ever logs on
# interactively. Granting them is what this run tests.
#
# Each of these is a real privilege being handed to a service account, so
# whichever ones turn out to be necessary become documented requirements of the
# v0.2 installer rather than something it does quietly.
foreach ($right in 'SeServiceLogonRight', 'SeBatchLogonRight', 'SeInteractiveLogonRight') {
    Grant-UserRight -Account $user -Right $right
}

# Stage the rootfs somewhere the account can read, and give it a writable place
# for the VHDX. Without this the import fails on permissions rather than on the
# question the spike is asking.
Write-Host "`n== staging rootfs for $user ==" -ForegroundColor Cyan
$rootfsSrc = Join-Path (Split-Path $here -Parent) 'a\dl\alpine-minirootfs-3.24.1-x86_64.tar.gz'
if (-not (Test-Path $rootfsSrc)) { throw "rootfs not found at $rootfsSrc - run 01-preflight.ps1 first" }
New-Item -ItemType Directory -Force $dataRoot | Out-Null
$rootfs = Join-Path $dataRoot 'rootfs.tar.gz'
Copy-Item $rootfsSrc $rootfs -Force
$vhd = Join-Path $dataRoot $distro
New-Item -ItemType Directory -Force $vhd | Out-Null
icacls $dataRoot /grant "${user}:(OI)(CI)F" /T | Out-Null
Write-Host "  rootfs at $rootfs, VHDX dir $vhd, both writable by $user" -ForegroundColor Green

Write-Host "`n== creating $svc under $user ==" -ForegroundColor Cyan
if (Get-Service $svc -ErrorAction SilentlyContinue) {
    sc.exe stop $svc | Out-Null
    Start-Sleep -Seconds 2
    sc.exe delete $svc | Out-Null
    Start-Sleep -Seconds 1
}
sc.exe create $svc binPath= "`"$exe`"" start= demand obj= ".\$user" password= "$pw" `
    DisplayName= "Skrog Spike B (dedicated account)" | Out-Null
if ($LASTEXITCODE -ne 0) { throw "sc create failed" }

# The probe reads these; SKROG_SPIKE_ROOTFS is what tells it to self-import.
$key = "HKLM:\SYSTEM\CurrentControlSet\Services\$svc"
Set-ItemProperty $key -Name Environment -Type MultiString -Value @(
    "SKROG_SPIKE_DISTRO=$distro",
    "SKROG_SPIKE_ROOTFS=$rootfs",
    "SKROG_SPIKE_VHD=$vhd"
)

$pw = $null  # drop it from this session

Write-Host "`n== starting ==" -ForegroundColor Cyan
sc.exe start $svc
Write-Host "waiting for the import and engine start (up to 2 min)..." -ForegroundColor DarkGray
Start-Sleep -Seconds 45
sc.exe query $svc | Select-String "STATE|WIN32_EXIT_CODE"

Write-Host "`n== probe log ==" -ForegroundColor Cyan
& (Join-Path $here 'show-log.ps1') -Mode service

Write-Host @"

The decisive lines:

  self-import-result          can a session-0 service account register a distro?
  distro-visible-after-import does it then see it?
  socket-up                   does dockerd start under that account?

All three ok -> the dedicated-account pattern works. Run 04 to test the
no-login window, which is the part that actually matters.

Any of them failing -> session-0 operation is not achievable on this WSL.
That is a NO-GO for PLAN section 06's headline claim, and worth knowing now.

Next: .\04-nologin-window.ps1
"@ -ForegroundColor Green
