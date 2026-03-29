param(
  [Parameter(Mandatory = $true)]
  [string]$Version,

  [Parameter(Mandatory = $true)]
  [ValidateSet("amd64")]
  [string]$Arch,

  [Parameter(Mandatory = $true)]
  [string]$GuiBinary,

  [Parameter(Mandatory = $true)]
  [string]$CliBinary,

  [Parameter(Mandatory = $true)]
  [string]$OutputFile
)

$ErrorActionPreference = "Stop"
$RootDir = Split-Path -Parent $PSScriptRoot
$StageDir = Join-Path ([System.IO.Path]::GetTempPath()) ("snt-installer-" + [System.Guid]::NewGuid().ToString("N"))
$IssPath = Join-Path $StageDir "installer.iss"
$InstallerDir = Split-Path -Parent $OutputFile

New-Item -ItemType Directory -Force -Path $StageDir | Out-Null
New-Item -ItemType Directory -Force -Path $InstallerDir | Out-Null

try {
  Copy-Item $GuiBinary (Join-Path $StageDir "snt-gui.exe")
  Copy-Item $CliBinary (Join-Path $StageDir "snt.exe")
  Copy-Item (Join-Path $RootDir "README.md") (Join-Path $StageDir "README.md")
  Copy-Item (Join-Path $RootDir "docs/DEPLOYMENT.md") (Join-Path $StageDir "DEPLOYMENT.md")
  Copy-Item (Join-Path $RootDir "docs/USER_GUIDE.md") (Join-Path $StageDir "USER_GUIDE.md")
  Copy-Item (Join-Path $RootDir "docs/GUI_CLIENT.md") (Join-Path $StageDir "GUI_CLIENT.md")
  Copy-Item (Join-Path $RootDir "examples/client-windows.json") (Join-Path $StageDir "client.example.json")
  Copy-Item (Join-Path $RootDir "scripts/run-gui.ps1") (Join-Path $StageDir "run-gui.ps1")

  $ArchitecturesAllowed = "x64os"
  $OutputBaseName = [System.IO.Path]::GetFileNameWithoutExtension($OutputFile)

  @"
#define MyAppName "Simple NAT Traversal"
#define MyAppVersion "$Version"
#define MyAppPublisher "zhiying8710"
#define MyStageDir "$($StageDir -replace '\\','\\')"

[Setup]
AppId={{B749C145-7AFB-4F7F-8F8B-2DD8C710C0B2}
AppName={#MyAppName}
AppVersion={#MyAppVersion}
AppPublisher={#MyAppPublisher}
DefaultDirName={autopf}\Simple NAT Traversal
DefaultGroupName=Simple NAT Traversal
DisableProgramGroupPage=yes
ArchitecturesAllowed=$ArchitecturesAllowed
ArchitecturesInstallIn64BitMode=$ArchitecturesAllowed
Compression=lzma2/ultra64
SolidCompression=yes
WizardStyle=modern
OutputDir=$($InstallerDir -replace '\\','\\')
OutputBaseFilename=$OutputBaseName
UninstallDisplayIcon={app}\snt-gui.exe

[Files]
Source: "{#MyStageDir}\snt-gui.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#MyStageDir}\snt.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#MyStageDir}\README.md"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#MyStageDir}\DEPLOYMENT.md"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#MyStageDir}\USER_GUIDE.md"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#MyStageDir}\GUI_CLIENT.md"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#MyStageDir}\client.example.json"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#MyStageDir}\run-gui.ps1"; DestDir: "{app}"; Flags: ignoreversion

[Icons]
Name: "{autoprograms}\Simple NAT Traversal"; Filename: "{app}\snt-gui.exe"
Name: "{autodesktop}\Simple NAT Traversal"; Filename: "{app}\snt-gui.exe"; Tasks: desktopicon
Name: "{autoprograms}\Simple NAT Traversal CLI"; Filename: "{cmd}"; Parameters: "/K ""{app}\snt.exe -version"""

[Tasks]
Name: "desktopicon"; Description: "Create a desktop shortcut"; GroupDescription: "Additional icons:"

[Run]
Filename: "{app}\snt-gui.exe"; Description: "Launch Simple NAT Traversal"; Flags: nowait postinstall skipifsilent
"@ | Set-Content -Path $IssPath -Encoding UTF8

  $iscc = Join-Path ${env:ProgramFiles(x86)} "Inno Setup 6\ISCC.exe"
  if (-not (Test-Path $iscc)) {
    throw "Inno Setup compiler not found at $iscc"
  }
  & $iscc $IssPath | Out-Host
} finally {
  Remove-Item -Recurse -Force $StageDir -ErrorAction SilentlyContinue
}
