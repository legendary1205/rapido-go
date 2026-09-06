//go:build !linux

package hostmetrics

// readDisk is a stub on any non-Linux GOOS (this whole package reads
// /proc, which only exists on Linux anyway) - it exists purely so
// `go build`/`go vet`/`go test` succeed on a Windows/macOS dev machine.
// The real implementation, disk_linux.go, is what actually runs once
// cross-compiled to the node agent or panel's real Linux deployment.
func readDisk() (total, used int64) {
	return 0, 0
}
