#requires -RunAsAdministrator
<#
.SYNOPSIS
  Installs the GameNoLag service on this machine.

.DESCRIPTION
  Three things have to be true before the service can work, and this sets all
  three:

    - The binary is somewhere only administrators can write. A service running as
      LocalSystem whose binary an ordinary user can replace is a way for that
      user to become LocalSystem.
    - The data directory is readable only by SYSTEM and Administrators. It holds
      this machine's private key and the contributor key.
    - The service is registered to start automatically, because a player who
      reboots should not have to remember to start anything.

  It does not start the tunnel. Nothing happens on this machine until the user
  interface asks for a connect.

.PARAMETER ContributorKey
  The key issued when a VPS was contributed.

.PARAMETER ControlUrl
  The control plane, https only.
#>
param(
  [Parameter(Mandatory = $true)][string]$ContributorKey,
  [Parameter(Mandatory = $true)][string]$ControlUrl,
  [string]$SourceDir = $PSScriptRoot
)

$ErrorActionPreference = 'Stop'

$serviceName = 'GameNoLag'
$installDir  = Join-Path $env:ProgramFiles 'GameNoLag'
$dataDir     = Join-Path $env:ProgramData  'GameNoLag'
$binary      = Join-Path $installDir 'gnl-service.exe'

# The service and the tray interface, plus the manifest the interface needs:
# without it the tray menu will not create, and it is read from beside the exe
# because Go binaries carry no embedded manifest.
$payload = @('gnl-service.exe', 'gnl-ui.exe', 'gnl-ui.exe.manifest')

if ($ControlUrl -notmatch '^https://') {
  throw "ControlUrl must be https: the contributor key would otherwise travel in clear."
}
foreach ($file in $payload) {
  if (-not (Test-Path (Join-Path $SourceDir $file))) {
    throw "Cannot find $file in $SourceDir. Build with:`n" +
          "  GOOS=windows GOARCH=amd64 go build -o gnl-service.exe ./cmd/gnl-service`n" +
          "  GOOS=windows GOARCH=amd64 go build -ldflags -H=windowsgui -o gnl-ui.exe ./cmd/gnl-ui`n" +
          "  cp deploy/windows/gnl-ui.exe.manifest ."
  }
}

# Stop an earlier install before replacing its binary. A running service holds
# the file open, and the copy would fail half way.
$existing = Get-Service -Name $serviceName -ErrorAction SilentlyContinue
if ($existing) {
  Write-Host "Stopping the existing $serviceName service"
  Stop-Service -Name $serviceName -Force -ErrorAction SilentlyContinue
  sc.exe delete $serviceName | Out-Null
  Start-Sleep -Seconds 2
}

# The tray holds gnl-ui.exe open the same way. The setup wizard starts it again
# for the player at the end; from a shell, it comes back at the next logon or
# from the Start Menu.
Get-Process -Name 'gnl-ui' -ErrorAction SilentlyContinue | Stop-Process -Force

New-Item -ItemType Directory -Force -Path $installDir, $dataDir | Out-Null
foreach ($file in $payload) {
  Copy-Item -Path (Join-Path $SourceDir $file) -Destination (Join-Path $installDir $file) -Force
}

# Both directories: inherited permissions from ProgramData let authenticated
# users create files, and the data directory holds a private key.
foreach ($dir in @($installDir, $dataDir)) {
  icacls $dir /inheritance:r | Out-Null
  icacls $dir /grant:r 'SYSTEM:(OI)(CI)F' 'Administrators:(OI)(CI)F' | Out-Null
}
# The install directory is read and execute for everyone, so the user interface
# can launch from it; the data directory is not readable at all.
icacls $installDir /grant:r 'Users:(OI)(CI)RX' | Out-Null

$config = [ordered]@{
  control_url     = $ControlUrl
  contributor_key = $ContributorKey
}
$configPath = Join-Path $dataDir 'config.json'
# Without a byte order mark: Windows PowerShell's -Encoding UTF8 adds one, and
# JSON does not allow it. The service tolerates it, but a file it has to forgive
# is a file other tools will choke on.
[System.IO.File]::WriteAllText($configPath, ($config | ConvertTo-Json),
  (New-Object System.Text.UTF8Encoding $false))

sc.exe create $serviceName binPath= "`"$binary`"" start= auto obj= LocalSystem DisplayName= "GameNoLag" | Out-Null
sc.exe description $serviceName "Routes game traffic through a GameNoLag relay." | Out-Null
# Restart on failure, twice, then leave it alone. A service that crash-loops
# forever writes log faster than anyone can read it, and the routes it installs
# are non-persistent so the machine is fine without it.
sc.exe failure $serviceName reset= 86400 actions= restart/5000/restart/20000/"" | Out-Null

# The tray interface starts for whoever logs in. It holds no privilege, so this
# is HKLM rather than per-user only because the service is machine-wide too.
$runKey = 'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Run'
Set-ItemProperty -Path $runKey -Name 'GameNoLag' -Value "`"$(Join-Path $installDir 'gnl-ui.exe')`""

$startMenu = Join-Path $env:ProgramData 'Microsoft\Windows\Start Menu\Programs\GameNoLag.lnk'
$shortcut = (New-Object -ComObject WScript.Shell).CreateShortcut($startMenu)
$shortcut.TargetPath = Join-Path $installDir 'gnl-ui.exe'
$shortcut.Description = 'GameNoLag'
$shortcut.Save()

Start-Service -Name $serviceName

Write-Host "Installed. Log: $(Join-Path $dataDir 'service.log')"
# Deliberately not started from here. This script runs elevated, so anything it
# launches runs elevated too, and the tray icon would appear in the
# administrator's session rather than the player's. It holds no privilege and
# should not have any.
Write-Host "Start GameNoLag from the Start Menu; it will start by itself at every logon after this."
