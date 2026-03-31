param(
  [Parameter(Mandatory = $true)][string]$Version,
  [Parameter(Mandatory = $true)][string]$DesktopBinary,
  [Parameter(Mandatory = $true)][string]$AgentBinary,
  [Parameter(Mandatory = $true)][string]$OutZip
)

$ErrorActionPreference = "Stop"

$tempDir = New-Item -ItemType Directory -Path ([System.IO.Path]::Combine([System.IO.Path]::GetTempPath(), [System.Guid]::NewGuid().ToString())) | Select-Object -ExpandProperty FullName
$packageRoot = Join-Path $tempDir ("minipunch-windows-" + $Version)
$binDir = Join-Path $packageRoot "bin"
New-Item -ItemType Directory -Force -Path $binDir | Out-Null

Copy-Item $DesktopBinary (Join-Path $binDir "minipunch-desktop.exe")
Copy-Item $AgentBinary (Join-Path $binDir "minipunch-agent.exe")

@"
MiniPunch Desktop $Version

Files:
- bin\minipunch-desktop.exe
- bin\minipunch-agent.exe

Notes:
- Autostart uses the current user's Startup folder by writing a small .cmd launcher.
- This package is a zip bundle, not an MSI installer.
"@ | Set-Content -Encoding UTF8 (Join-Path $packageRoot "README.txt")

$outDir = Split-Path -Parent $OutZip
if ($outDir -and -not (Test-Path $outDir)) {
  New-Item -ItemType Directory -Force -Path $outDir | Out-Null
}
if (Test-Path $OutZip) {
  Remove-Item -Force $OutZip
}

Compress-Archive -Path $packageRoot -DestinationPath $OutZip
Remove-Item -Recurse -Force $tempDir

Write-Output "created $OutZip"
