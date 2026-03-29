param(
  [string]$ConfigPath = ""
)

$RootDir = Split-Path -Parent $PSScriptRoot
$sameDir = Join-Path $PSScriptRoot "snt-gui.exe"
$parentDir = Join-Path $RootDir "snt-gui.exe"

if (Test-Path $sameDir) {
  $GuiPath = $sameDir
} elseif (Test-Path $parentDir) {
  $GuiPath = $parentDir
} else {
  throw "snt-gui.exe not found next to launcher or in parent directory"
}

if ([string]::IsNullOrWhiteSpace($ConfigPath)) {
  & $GuiPath
} else {
  & $GuiPath -config $ConfigPath
}
