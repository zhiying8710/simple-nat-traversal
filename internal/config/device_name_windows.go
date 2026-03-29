//go:build windows

package config

import (
	"strings"

	"golang.org/x/sys/windows/registry"
)

func platformVersionLabel() string {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows NT\CurrentVersion`, registry.QUERY_VALUE)
	if err != nil {
		return "windows"
	}
	defer key.Close()

	for _, name := range []string{"ProductName", "DisplayVersion", "CurrentVersion"} {
		if value, _, err := key.GetStringValue(name); err == nil {
			value = strings.TrimSpace(value)
			if value != "" {
				return value
			}
		}
	}
	return "windows"
}
