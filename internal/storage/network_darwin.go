//go:build darwin

package storage

import (
	"fmt"
	"slices"
	"strings"
	"syscall"
)

func isNetworkPath(path string) (bool, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return false, fmt.Errorf("statfs %s: %w", path, err)
	}

	var name strings.Builder
	for _, c := range st.Fstypename {
		if c == 0 {
			break
		}
		name.WriteByte(byte(c))
	}
	return slices.Contains([]string{"nfs", "smbfs", "afpfs", "webdav"}, name.String()), nil
}
