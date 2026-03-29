package client

import "time"

func max(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}
