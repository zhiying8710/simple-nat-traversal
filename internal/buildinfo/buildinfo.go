package buildinfo

import "fmt"

var (
	Version = "dev"
	Commit  = "unknown"
	BuiltAt = "unknown"
)

func String(binary string) string {
	return fmt.Sprintf("%s version=%s commit=%s built_at=%s", binary, Version, Commit, BuiltAt)
}
