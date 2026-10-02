//go:build unix

package hwmonreceiver

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// sysReadFile is a simplified os.ReadFile that invokes syscall.Read directly.
func sysReadFile(file string) ([]byte, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// On some machines, hwmon drivers are broken and return EAGAIN.  This causes
	// Go's os.ReadFile implementation to poll forever.
	//
	// Since we either want to read data or bail immediately, do the simplest
	// possible read using system call directly.
	b := make([]byte, 128)
	n, err := unix.Read(int(f.Fd()), b)
	if err != nil {
		return nil, err
	}
	if n < 0 {
		return nil, fmt.Errorf("failed to read file: %q, read returned negative bytes value: %d", file, n)
	}

	return b[:n], nil
}
