//go:build unix

package main

import (
	"os"
)

// secureInstancesDir restricts path to the current user (0700).
func secureInstancesDir(path string) error {
	return os.Chmod(path, 0700)
}

// secureInstanceFile restricts path to the current user (0600).
func secureInstanceFile(path string) error {
	return os.Chmod(path, 0600)
}
