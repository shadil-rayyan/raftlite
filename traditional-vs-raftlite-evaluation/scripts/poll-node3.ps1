# Diagnostic: poll node3 during the partition experiment and log what the
# catch-up check would see (status + how many of the run's IPs are present).
$prefix = $args[0]   # e.g. 100.9
$out = Join-Path $PSScriptRoot "..\node3-poll.log"
$deadline = (Get-Date).AddSeconds(150)
while ((Get-Date) -lt $deadline) {
    $line = ""
    try {
        $r = Invoke-WebRequest -UseBasicParsing -TimeoutSec 5 'http://127.0.0.1:8083/v1/policies?consistency=stale'
        $j = $r.Content | ConvertFrom-Json
        $mine = @($j.policies | Where-Object { $_.ip -like "$prefix.*" }).Count
        $line = "{0:HH:mm:ss.fff} HTTP={1} total={2} run_ips={3}" -f (Get-Date), $r.StatusCode, $j.policies.Count, $mine
    } catch {
        $line = "{0:HH:mm:ss.fff} ERROR {1}" -f (Get-Date), $_.Exception.Message
    }
    Add-Content -Path $out -Value $line
    Start-Sleep -Seconds 1
}
