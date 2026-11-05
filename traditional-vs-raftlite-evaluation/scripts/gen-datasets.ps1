# gen-datasets.ps1 — regenerate the shared datasets.
# Usage:  .\scripts\gen-datasets.ps1 [-Max 10000]
param([int]$Max = 10000)
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot

Push-Location $root
try {
    go run ./cmd/gen -out datasets -max $Max
    if ($LASTEXITCODE -ne 0) { throw "dataset generation failed" }
}
finally { Pop-Location }

Get-ChildItem "$root\datasets" | Select-Object Name, Length
