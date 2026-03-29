package config

import (
	"crypto/rand"
	"encoding/base32"
	"os/user"
	"regexp"
	"strings"
)

var nonDeviceNameChars = regexp.MustCompile(`[^a-z0-9-]+`)

func SuggestedDeviceName() string {
	return buildSuggestedDeviceName(platformVersionLabel(), currentUsername(), randomDeviceSuffix(6))
}

func buildSuggestedDeviceName(platformVersion, username, suffix string) string {
	platformPart := sanitizeDeviceNamePart(platformVersion, 18)
	userPart := sanitizeDeviceNamePart(username, 18)
	suffixPart := sanitizeDeviceNamePart(suffix, 6)

	parts := make([]string, 0, 3)
	if platformPart != "" {
		parts = append(parts, platformPart)
	}
	if userPart != "" {
		parts = append(parts, userPart)
	}
	if suffixPart != "" {
		parts = append(parts, suffixPart)
	}
	if len(parts) == 0 {
		return "device"
	}
	return strings.Join(parts, "-")
}

func currentUsername() string {
	if current, err := user.Current(); err == nil {
		for _, value := range []string{current.Username, current.Name} {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
		}
	}
	return "user"
}

func randomDeviceSuffix(length int) string {
	if length <= 0 {
		length = 6
	}
	buf := make([]byte, length)
	if _, err := rand.Read(buf); err != nil {
		return "random"
	}
	encoded := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf))
	if len(encoded) < length {
		return encoded
	}
	return encoded[:length]
}

func sanitizeDeviceNamePart(raw string, maxLen int) string {
	normalized := strings.ToLower(strings.TrimSpace(raw))
	normalized = strings.ReplaceAll(normalized, "_", "-")
	normalized = strings.ReplaceAll(normalized, ".", "-")
	normalized = strings.ReplaceAll(normalized, " ", "-")
	normalized = nonDeviceNameChars.ReplaceAllString(normalized, "-")
	normalized = strings.Trim(normalized, "-")
	normalized = strings.TrimPrefix(normalized, "microsoft-")
	normalized = strings.TrimPrefix(normalized, "windows-")
	normalized = strings.TrimPrefix(normalized, "macos-")
	if maxLen > 0 && len(normalized) > maxLen {
		normalized = normalized[:maxLen]
		normalized = strings.Trim(normalized, "-")
	}
	return normalized
}
