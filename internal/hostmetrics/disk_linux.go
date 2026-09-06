//go:build linux

package hostmetrics

import (
	"os"
	"syscall"
)

// readDisk statfs's diskPath (falling back to "/") - only meaningful on
// Linux, which is the only platform the node agent or panel ever actually
// runs on. See disk_other.go for the stub every other GOOS gets, so
// `go build`/`go vet` still pass on a Windows dev machine.
func readDisk() (total, used int64) {
	path := diskPath
	if _, err := os.Stat(path); err != nil {
		path = "/"
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, 0
	}
	total = int64(stat.Blocks) * int64(stat.Bsize)
	free := int64(stat.Bavail) * int64(stat.Bsize)
	return total, total - free
}
