# build.ps1 — build every evaluation component (and the raftlite binary).
# Usage:  .\scripts\build.ps1
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot

New-Item -ItemType Directory -Force -Path "$root\bin" | Out-Null

Push-Location $root
try {
    go build -o bin/ ./cmd/...
    if ($LASTEXITCODE -ne 0) { throw "eval build failed" }
}
finally { Pop-Location }

# The RaftLite binary comes from the parent repository (this folder is nested
# inside raftlite). Building it here keeps the evaluation self-contained at
# runtime while still exercising the real RaftLite implementation.
$raftliteRoot = Split-Path -Parent $root
if (Test-Path "$raftliteRoot\cmd\raftlite") {
    Push-Location $raftliteRoot
    try {
        go build -o "$root\bin\raftlite.exe" ./cmd/raftlite
        if ($LASTEXITCODE -ne 0) { throw "raftlite build failed" }
    }
    finally { Pop-Location }
    Write-Output "built raftlite.exe"
}
else {
    Write-Warning "raftlite source not found at $raftliteRoot\cmd\raftlite — the raftlite environment will not start"
}

Write-Output "built into $root\bin"
Get-ChildItem "$root\bin" | Select-Object Name, Length
