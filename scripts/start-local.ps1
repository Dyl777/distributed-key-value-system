# scripts/start-local.ps1
# Start a small local cluster bound to a LAN IP and advertise LAN addresses via /join
# Usage: edit $LanIP then run in PowerShell (run as your user):
#   ./scripts/start-local.ps1

param(
    [string]$LanIP = "192.168.1.100",
    [int[]]$Ports = @(8000,8001,8002)
)

# Build nodes CSV using LAN IP
$nodes = $Ports | ForEach-Object { "http://$LanIP:`$_" } -join ","
Write-Host "Nodes list: $nodes"

# Start each node in a new PowerShell window (go run . -port=... -nodes=...)
foreach ($p in $Ports) {
    $cmd = "go run . -port=$p -nodes=$nodes -rep=3 -W=2 -R=1"
    Write-Host "Starting node on port $p: $cmd"
    Start-Process -FilePath "powershell" -ArgumentList "-NoExit","-Command",$cmd
    Start-Sleep -Milliseconds 300
}

Start-Sleep -Seconds 2

# Notify seed with advertised LAN URLs by POSTing to /join using LAN addresses.
# This ensures the seed records the advertised LAN URLs rather than 'localhost'.
$seedPort = $Ports[0]
foreach ($p in $Ports[1..($Ports.Length - 1)]) {
    $seedUrl = "http://$LanIP:$seedPort/join"
    $body = "http://$LanIP:$p"
    Write-Host "Posting join for $body to $seedUrl"
    try {
        Invoke-RestMethod -Method Post -Uri $seedUrl -Body $body
        Write-Host "Joined $body to seed $seedUrl"
    } catch {
        Write-Warning "Join failed for $body: $_"
    }
}

Write-Host "Started $($Ports.Length) nodes (each in its own window)."