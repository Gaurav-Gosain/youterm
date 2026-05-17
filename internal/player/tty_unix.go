//go:build linux || darwin

package player

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"unsafe"
)

func openTTY() (*os.File, error) {
	return os.OpenFile("/dev/tty", os.O_RDWR, 0)
}

func getWinsize(fd int) winsize {
	var ws winsize
	_, _, _ = syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), syscall.TIOCGWINSZ, uintptr(unsafe.Pointer(&ws)))
	return ws
}

func watchResize(ctx context.Context, onResize func()) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGWINCH)
	go func() {
		defer signal.Stop(sigCh)
		for {
			select {
			case <-ctx.Done():
				return
			case <-sigCh:
				onResize()
			}
		}
	}()
}
