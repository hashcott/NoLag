#requires -RunAsAdministrator
<#
.SYNOPSIS
  Removes the GameNoLag service from this machine.

.DESCRIPTION
  Stopping the service is what removes its effect on the network: the adapter
  disappears with the process and every route pointing at it goes too, so no
  route cleanup is needed or possible here.

  The device key is kept by default. Deleting it means the next install
  activates as a new machine and consumes another device slot on the
  contributor's key, so -Purge has to be asked for.
#>
param([switch]$Purge)

$ErrorActionPreference = 'Stop'

$serviceName = 'GameNoLag'
$installDir  = Join-Path $env:ProgramFiles 'GameNoLag'
$dataDir     = Join-Path $env:ProgramData  'GameNoLag'

if (Get-Service -Name $serviceName -ErrorAction SilentlyContinue) {
  Stop-Service -Name $serviceName -Force -ErrorAction SilentlyContinue
  sc.exe delete $serviceName | Out-Null
  Start-Sleep -Seconds 2
} else {
  Write-Host "The $serviceName service is not installed."
}

Remove-Item -Recurse -Force -Path $installDir -ErrorAction SilentlyContinue

if ($Purge) {
  Remove-Item -Recurse -Force -Path $dataDir -ErrorAction SilentlyContinue
  Write-Host "Removed, including this machine's identity. Reinstalling will use another device slot."
} else {
  Write-Host "Removed. Kept $dataDir so a reinstall keeps this machine's device slot; -Purge deletes it."
}
