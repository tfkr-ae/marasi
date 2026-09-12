//go:build unix

package main

import (
	"os"
)

func secureInstancesDir(path string) error {
	return os.Chmod(path, 0700)
}

func secureInstanceFile(path string) error {
	return os.Chmod(path, 0600)
}
