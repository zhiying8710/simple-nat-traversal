//go:build !darwin && !windows

package config

import "runtime"

func platformVersionLabel() string {
	return runtime.GOOS
}
