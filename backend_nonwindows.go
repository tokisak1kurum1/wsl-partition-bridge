//go:build !windows

package main

import "fmt"

func openWindowsRawBackend(disk int, offset, size int64) (Backend, error) {
	return nil, fmt.Errorf("Windows only")
}
