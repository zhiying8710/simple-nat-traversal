package main

import (
	"fmt"
	"os"
	"strings"
)

func resolveOptionalSecret(envName, filePath, label string) (*string, error) {
	if strings.TrimSpace(envName) != "" && strings.TrimSpace(filePath) != "" {
		return nil, fmt.Errorf("choose only one of -set-%s-env or -set-%s-file", label, label)
	}
	if strings.TrimSpace(envName) != "" {
		value, ok := os.LookupEnv(envName)
		if !ok {
			return nil, fmt.Errorf("environment variable %s is not set", envName)
		}
		return &value, nil
	}
	if strings.TrimSpace(filePath) != "" {
		raw, err := os.ReadFile(filePath)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", filePath, err)
		}
		value := strings.TrimRight(string(raw), "\r\n")
		return &value, nil
	}
	return nil, nil
}
