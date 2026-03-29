package autostart

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	macLaunchAgentLabel = "com.simple-nat-traversal.snt"
	macLaunchAgentFile  = "com.simple-nat-traversal.snt.plist"
	winStartupFile      = "simple-nat-traversal-snt.vbs"
)

type Status struct {
	Platform       string `json:"platform"`
	Installed      bool   `json:"installed"`
	FilePath       string `json:"file_path"`
	ExecutablePath string `json:"executable_path,omitempty"`
	ConfigPath     string `json:"config_path,omitempty"`
	LaunchCommand  string `json:"launch_command,omitempty"`
}

type spec struct {
	filePath      string
	content       string
	launchCommand string
}

func Install(executablePath, configPath string) (Status, error) {
	if isEphemeralExecutable(executablePath) {
		return Status{}, errors.New("autostart install requires a stable built binary, not a temporary go run executable")
	}
	spec, err := buildSpec(runtime.GOOS, executablePath, configPath)
	if err != nil {
		return Status{}, err
	}
	if err := os.MkdirAll(filepath.Dir(spec.filePath), 0o755); err != nil {
		return Status{}, fmt.Errorf("create autostart dir: %w", err)
	}
	if err := os.WriteFile(spec.filePath, []byte(spec.content), 0o644); err != nil {
		return Status{}, fmt.Errorf("write autostart file: %w", err)
	}
	return statusFromSpec(spec, true), nil
}

func StatusFor(executablePath, configPath string) (Status, error) {
	spec, err := buildSpec(runtime.GOOS, executablePath, configPath)
	if err != nil {
		return Status{}, err
	}
	_, statErr := os.Stat(spec.filePath)
	switch {
	case statErr == nil:
		return statusFromSpec(spec, true), nil
	case errors.Is(statErr, os.ErrNotExist):
		return statusFromSpec(spec, false), nil
	default:
		return Status{}, fmt.Errorf("stat autostart file: %w", statErr)
	}
}

func Uninstall() (Status, error) {
	filePath, err := autostartFilePath(runtime.GOOS)
	if err != nil {
		return Status{}, err
	}
	err = os.Remove(filePath)
	switch {
	case err == nil || errors.Is(err, os.ErrNotExist):
		return Status{
			Platform:  runtime.GOOS,
			Installed: false,
			FilePath:  filePath,
		}, nil
	default:
		return Status{}, fmt.Errorf("remove autostart file: %w", err)
	}
}

func RenderStatus(status Status) string {
	var out strings.Builder
	fmt.Fprintf(&out, "platform\t%s\n", status.Platform)
	if status.Installed {
		fmt.Fprintf(&out, "installed\tyes\n")
	} else {
		fmt.Fprintf(&out, "installed\tno\n")
	}
	fmt.Fprintf(&out, "file\t%s\n", dash(status.FilePath))
	fmt.Fprintf(&out, "executable\t%s\n", dash(status.ExecutablePath))
	fmt.Fprintf(&out, "config\t%s\n", dash(status.ConfigPath))
	fmt.Fprintf(&out, "command\t%s\n", dash(status.LaunchCommand))
	return out.String()
}

func buildSpec(goos, executablePath, configPath string) (spec, error) {
	exeAbs, err := filepath.Abs(executablePath)
	if err != nil {
		return spec{}, fmt.Errorf("resolve executable path: %w", err)
	}
	cfgAbs, err := filepath.Abs(configPath)
	if err != nil {
		return spec{}, fmt.Errorf("resolve config path: %w", err)
	}

	filePath, err := autostartFilePath(goos)
	if err != nil {
		return spec{}, err
	}

	switch goos {
	case "darwin":
		return spec{
			filePath:      filePath,
			content:       macLaunchAgentContents(exeAbs, cfgAbs),
			launchCommand: fmt.Sprintf("%q -config %q", exeAbs, cfgAbs),
		}, nil
	case "windows":
		return spec{
			filePath:      filePath,
			content:       windowsStartupScript(exeAbs, cfgAbs),
			launchCommand: fmt.Sprintf("%q -config %q", exeAbs, cfgAbs),
		}, nil
	default:
		return spec{}, fmt.Errorf("autostart is only implemented for macOS and Windows, current platform=%s", goos)
	}
}

func autostartFilePath(goos string) (string, error) {
	switch goos {
	case "darwin":
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve user home: %w", err)
		}
		return filepath.Join(homeDir, "Library", "LaunchAgents", macLaunchAgentFile), nil
	case "windows":
		appData := strings.TrimSpace(os.Getenv("APPDATA"))
		if appData == "" {
			return "", errors.New("APPDATA is not set")
		}
		return filepath.Join(appData, "Microsoft", "Windows", "Start Menu", "Programs", "Startup", winStartupFile), nil
	default:
		return "", fmt.Errorf("autostart is only implemented for macOS and Windows, current platform=%s", goos)
	}
}

func statusFromSpec(spec spec, installed bool) Status {
	return Status{
		Platform:       runtime.GOOS,
		Installed:      installed,
		FilePath:       spec.filePath,
		ExecutablePath: firstLineArg(spec.launchCommand, 0),
		ConfigPath:     firstLineArg(spec.launchCommand, 1),
		LaunchCommand:  spec.launchCommand,
	}
}

func macLaunchAgentContents(executablePath, configPath string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>%s</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
    <string>-config</string>
    <string>%s</string>
  </array>
  <key>RunAtLoad</key>
  <true/>
</dict>
</plist>
`, xmlEscape(macLaunchAgentLabel), xmlEscape(executablePath), xmlEscape(configPath))
}

func windowsStartupScript(executablePath, configPath string) string {
	return fmt.Sprintf(`Set shell = CreateObject("WScript.Shell")
shell.Run Chr(34) & "%s" & Chr(34) & " -config " & Chr(34) & "%s" & Chr(34), 0
Set shell = Nothing
`, vbsEscape(executablePath), vbsEscape(configPath))
}

func xmlEscape(value string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&apos;",
	)
	return replacer.Replace(value)
}

func vbsEscape(value string) string {
	return strings.ReplaceAll(value, `"`, `""`)
}

func firstLineArg(command string, index int) string {
	parts := strings.Split(command, `"`)
	if len(parts) < 4 {
		return ""
	}
	switch index {
	case 0:
		return parts[1]
	case 1:
		if len(parts) >= 4 {
			return parts[3]
		}
	}
	return ""
}

func dash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}

func isEphemeralExecutable(path string) bool {
	clean := strings.ToLower(filepath.Clean(path))
	return strings.Contains(clean, "go-build") || strings.Contains(clean, string(filepath.Separator)+"tmp"+string(filepath.Separator)+"go-build")
}
