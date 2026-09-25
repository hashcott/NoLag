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
  [string]$BinarySource = "$PSScriptRoot\gnl-service.exe"
)

$ErrorActionPreference = 'Stop'

$serviceName = 'GameNoLag'
$installDir  = Join-Path $env:ProgramFiles 'GameNoLag'
$dataDir     = Join-Path $env:ProgramData  'GameNoLag'
$binary      = Join-Path $installDir 'gnl-service.exe'

if ($ControlUrl -notmatch '^https://') {
  throw "ControlUrl must be https: the contributor key would otherwise travel in clear."
}
if (-not (Test-Path $BinarySource)) {
  throw "Cannot find $BinarySource. Build it with: GOOS=windows GOARCH=amd64 go build -o gnl-service.exe ./cmd/gnl-service"
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

New-Item -ItemType Directory -Force -Path $installDir, $dataDir | Out-Null
Copy-Item -Path $BinarySource -Destination $binary -Force

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
$config | ConvertTo-Json | Set-Content -Path $configPath -Encoding UTF8

sc.exe create $serviceName binPath= "`"$binary`"" start= auto obj= LocalSystem DisplayName= "GameNoLag" | Out-Null
sc.exe description $serviceName "Routes game traffic through a GameNoLag relay." | Out-Null
# Restart on failure, twice, then leave it alone. A service that crash-loops
# forever writes log faster than anyone can read it, and the routes it installs
# are non-persistent so the machine is fine without it.
sc.exe failure $serviceName reset= 86400 actions= restart/5000/restart/20000/"" | Out-Null

Start-Service -Name $serviceName
Write-Host "Installed. Log: $(Join-Path $dataDir 'service.log')"
