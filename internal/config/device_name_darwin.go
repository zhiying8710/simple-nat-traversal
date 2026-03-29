//go:build darwin

package config

import (
	"os/exec"
	"strings"
)

func platformVersionLabel() string {
	out, err := exec.Command("sw_vers", "-productVersion").Output()
	if err != nil {
		return "macos"
	}
	version := strings.TrimSpace(string(out))
	if version == "" {
		return "macos"
	}
	if major, _, ok := strings.Cut(version, "."); ok && strings.TrimSpace(major) != "" {
		return "macos" + major
	}
	return "macos" + strings.ReplaceAll(version, ".", "")
}
