param(
  [Parameter(Mandatory = $true)]
  [string]$Version
)

$ErrorActionPreference = "Stop"
$RootDir = Split-Path -Parent $PSScriptRoot
$DistDir = Join-Path $RootDir "dist\$Version"
$Commit = "unknown"
try {
  $Commit = (git -C $RootDir rev-parse --short HEAD).Trim()
} catch {
}
$BuiltAt = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")
$LdFlags = "-X simple-nat-traversal/internal/buildinfo.Version=$Version -X simple-nat-traversal/internal/buildinfo.Commit=$Commit -X simple-nat-traversal/internal/buildinfo.BuiltAt=$BuiltAt"
$HostGoos = (go env GOOS).Trim()
$HostGoarch = (go env GOARCH).Trim()

New-Item -ItemType Directory -Force -Path $DistDir | Out-Null

function Build-One {
  param(
    [string]$Goos,
    [string]$Goarch,
    [string]$Package,
    [string]$Name
  )

  $ext = ""
  if ($Goos -eq "windows") {
    $ext = ".exe"
  }
  $out = Join-Path $DistDir "$Name-$Version-$Goos-$Goarch$ext"
  Write-Host "building $out"
  $env:GOOS = $Goos
  $env:GOARCH = $Goarch
  go build -ldflags $LdFlags -o $out $Package
}

function Build-GuiIfNativeHost {
  param(
    [string]$Goos,
    [string]$Goarch,
    [string]$Package,
    [string]$Name
  )

  if ($HostGoos -ne $Goos -or $HostGoarch -ne $Goarch) {
    Write-Host "skipping $Name-$Version-$Goos-$Goarch: native $Goos/$Goarch build host required for Fyne GUI (current host $HostGoos/$HostGoarch)"
    return
  }

  Build-One $Goos $Goarch $Package $Name
}

Build-One linux amd64 ./cmd/snt-server snt-server
Build-One darwin arm64 ./cmd/snt snt
Build-One windows amd64 ./cmd/snt snt
Build-GuiIfNativeHost darwin arm64 ./cmd/snt-gui snt-gui
Build-GuiIfNativeHost windows amd64 ./cmd/snt-gui snt-gui

Remove-Item Env:GOOS -ErrorAction SilentlyContinue
Remove-Item Env:GOARCH -ErrorAction SilentlyContinue

Write-Host "release artifacts written to $DistDir"
