//go:build windows

package main

import (
	"fmt"
	"io"
	"sync"
	"syscall"
	"unsafe"
)

const (
	genericRead     = 0x80000000
	fileShareRead   = 0x00000001
	fileShareWrite  = 0x00000002
	fileShareDelete = 0x00000004
	openExisting    = 3
	fileBegin       = 0
)

var (
	kernel32             = syscall.NewLazyDLL("kernel32.dll")
	procCreateFileW      = kernel32.NewProc("CreateFileW")
	procSetFilePointerEx = kernel32.NewProc("SetFilePointerEx")
	procReadFile         = kernel32.NewProc("ReadFile")
	procCloseHandle      = kernel32.NewProc("CloseHandle")
)

type windowsRawBackend struct {
	h    syscall.Handle
	disk int
	base int64
	size int64
	mu   sync.Mutex
}

func openWindowsRawBackend(disk int, offset, size int64) (Backend, error) {
	path := fmt.Sprintf(`\\.\PhysicalDrive%d`, disk)
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	r, _, e := procCreateFileW.Call(
		uintptr(unsafe.Pointer(p)),
		uintptr(genericRead),
		uintptr(fileShareRead|fileShareWrite|fileShareDelete),
		0,
		uintptr(openExisting),
		0,
		0,
	)
	h := syscall.Handle(r)
	if h == syscall.InvalidHandle {
		return nil, fmt.Errorf("CreateFileW(%s): %w (run as Administrator)", path, e)
	}
	return &windowsRawBackend{h: h, disk: disk, base: offset, size: size}, nil
}

func (b *windowsRawBackend) Size() int64 { return b.size }
func (b *windowsRawBackend) Description() string {
	return fmt.Sprintf(`\\.\PhysicalDrive%d [offset=%d size=%d]`, b.disk, b.base, b.size)
}
func (b *windowsRawBackend) Close() error {
	if b.h == syscall.InvalidHandle {
		return nil
	}
	r, _, e := procCloseHandle.Call(uintptr(b.h))
	b.h = syscall.InvalidHandle
	if r == 0 {
		return e
	}
	return nil
}

func (b *windowsRawBackend) ReadAt(p []byte, off int64) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if off < 0 || off > b.size || int64(len(p)) > b.size-off {
		return 0, io.EOF
	}
	if len(p) == 0 {
		return 0, nil
	}
	absolute := b.base + off
	var newPos int64
	r, _, e := procSetFilePointerEx.Call(uintptr(b.h), uintptr(absolute), uintptr(unsafe.Pointer(&newPos)), uintptr(fileBegin))
	if r == 0 {
		return 0, fmt.Errorf("SetFilePointerEx: %w", e)
	}
	var n uint32
	r, _, e = procReadFile.Call(uintptr(b.h), uintptr(unsafe.Pointer(&p[0])), uintptr(uint32(len(p))), uintptr(unsafe.Pointer(&n)), 0)
	if r == 0 {
		return int(n), fmt.Errorf("ReadFile: %w", e)
	}
	if int(n) != len(p) {
		return int(n), io.EOF
	}
	return int(n), nil
}
