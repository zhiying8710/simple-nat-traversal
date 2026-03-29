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

function Build-WindowsGui {
  param(
    [string]$Goarch
  )

  $guiOut = Join-Path $DistDir "snt-gui-$Version-windows-$Goarch.exe"
  $cliOut = Join-Path $DistDir "snt-$Version-windows-$Goarch.exe"
  $installerOut = Join-Path $DistDir "client-windows-$Goarch-setup.exe"

  $cc = if ($Goarch -eq "arm64") { $env:SNT_WINDOWS_CC_ARM64 } else { $env:SNT_WINDOWS_CC_AMD64 }
  if ([string]::IsNullOrWhiteSpace($cc)) {
    $cc = if ($Goarch -eq "arm64") { "clang" } else { "gcc" }
  }
  if (-not (Get-Command $cc -ErrorAction SilentlyContinue)) {
    Write-Host "skipping Windows $Goarch GUI packaging: compiler '$cc' not found in PATH"
    return
  }

  Write-Host "building $cliOut"
  $env:GOOS = "windows"
  $env:GOARCH = $Goarch
  go build -ldflags $LdFlags -o $cliOut ./cmd/snt

  Write-Host "building $guiOut"
  $env:CGO_ENABLED = "1"
  $env:CC = $cc
  go build -ldflags $LdFlags -o $guiOut ./cmd/snt-gui
  Remove-Item Env:CGO_ENABLED -ErrorAction SilentlyContinue
  Remove-Item Env:CC -ErrorAction SilentlyContinue

  & (Join-Path $RootDir "scripts/package-windows-installer.ps1") `
    -Version $Version `
    -Arch $Goarch `
    -GuiBinary $guiOut `
    -CliBinary $cliOut `
    -OutputFile $installerOut
}

Build-One linux amd64 ./cmd/snt-server snt-server
Build-One linux amd64 ./cmd/snt snt

if ($HostGoos -eq "windows") {
  Build-WindowsGui amd64
  Build-WindowsGui arm64
} else {
  Write-Host "skipping Windows installer packaging: run this script on a Windows host with MinGW/Clang and Inno Setup installed"
}

Remove-Item Env:GOOS -ErrorAction SilentlyContinue
Remove-Item Env:GOARCH -ErrorAction SilentlyContinue
Remove-Item Env:CGO_ENABLED -ErrorAction SilentlyContinue
Remove-Item Env:CC -ErrorAction SilentlyContinue

Write-Host "release artifacts written to $DistDir"
