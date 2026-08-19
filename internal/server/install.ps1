# LanTally agent installer. Usage:
#   $env:LANTALLY_CLAIM='CODE'; irm http://SERVER:8080/install.ps1 | iex
$ErrorActionPreference = "Stop"
$ServerUrl = if ($env:LANTALLY_SERVER) { $env:LANTALLY_SERVER } else { "__SERVER_URL__" }
$Code = $env:LANTALLY_CLAIM
if (-not $Code) {
  $Code = Read-Host "LanTally claim code"
}
if (-not $Code) { throw "claim code required" }

$arch = "amd64"
if ($env:PROCESSOR_ARCHITECTURE -match "ARM") { $arch = "arm64" }
$binDir = Join-Path $env:LOCALAPPDATA "LanTally"
New-Item -ItemType Directory -Force -Path $binDir | Out-Null
$bin = Join-Path $binDir "lantally-agent.exe"
Invoke-WebRequest -UseBasicParsing -Uri "$ServerUrl/agents/windows/$arch" -OutFile $bin

$redeem = Invoke-RestMethod -Method Post -Uri "$ServerUrl/v1/claim" -ContentType "application/json" -Body (@{ code = $Code } | ConvertTo-Json)
$tokenFile = Join-Path $binDir "token"
Set-Content -Path $tokenFile -Value $redeem.token -Encoding ascii

$mihomoUrl = ""
$secretFile = Join-Path $binDir "mihomo.secret"
$candidates = @(
  (Join-Path $env:USERPROFILE ".config\mihomo\config.yaml"),
  (Join-Path $env:USERPROFILE ".config\clash\config.yaml")
)
foreach ($candidate in $candidates) {
  if (-not (Test-Path $candidate)) { continue }
  $text = Get-Content -Raw -Path $candidate
  $ext = [regex]::Match($text, '(?m)^external-controller:\s*(.+)$').Groups[1].Value.Trim().Trim('"')
  $secret = [regex]::Match($text, '(?m)^secret:\s*(.+)$').Groups[1].Value.Trim().Trim('"')
  if ($ext) {
    if ($ext.StartsWith(":")) { $mihomoUrl = "http://127.0.0.1$ext" } else { $mihomoUrl = "http://$ext" }
    if ($secret) {
      Set-Content -Path $secretFile -Value $secret -Encoding ascii
    }
    break
  }
}
if (-not $mihomoUrl -and $redeem.local) {
  $mihomoUrl = "http://127.0.0.1:9090"
}

$config = @{
  server_url = $ServerUrl
  site_id = $redeem.site_id
  node_id = $redeem.node_id
  token_file = $tokenFile
  interval = "15s"
  collectors = @{ iface = $true; mihomo = [bool]$mihomoUrl; nlbwmon = $false }
  mihomo = @{ url = $mihomoUrl; secret_file = $secretFile }
}
$configPath = Join-Path $binDir "agent.json"
$config | ConvertTo-Json | Set-Content -Path $configPath -Encoding utf8

$action = New-ScheduledTaskAction -Execute $bin -Argument "-config `"$configPath`""
$trigger = New-ScheduledTaskTrigger -AtLogOn
Register-ScheduledTask -TaskName "LanTallyAgent" -Action $action -Trigger $trigger -Force | Out-Null
Start-Process $bin -ArgumentList "-config `"$configPath`""
Write-Host "LanTally agent installed for $($redeem.node_id)"
