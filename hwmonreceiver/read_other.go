//go:build !unix

package hwmonreceiver

import "os"

// hwmon exists only on Linux; this keeps the module building elsewhere.
func sysReadFile(file string) ([]byte, error) {
	return os.ReadFile(file)
}
