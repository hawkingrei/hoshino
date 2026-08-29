//go:build !darwin && !linux

package diskutil

import "os"

func fileIdentity(os.FileInfo) (uint64, uint64, bool) {
	return 0, 0, false
}
