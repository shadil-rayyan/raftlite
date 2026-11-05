# start.ps1 — start the measurement server and open the dashboard.
# Usage:  .\scripts\start.ps1   (builds first if bin\measure.exe is missing)
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot

if (-not (Test-Path "$root\bin\measure.exe")) {
    & "$root\scripts\build.ps1"
}

# Stop a previous instance if one is still running.
Get-Process measure -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
Start-Sleep -Milliseconds 500

Start-Process -FilePath "$root\bin\measure.exe" -WorkingDirectory $root -WindowStyle Hidden
Start-Sleep -Seconds 2

Write-Output "measurement server: http://127.0.0.1:8095"
Start-Process "http://127.0.0.1:8095"
