// Package filelock provides nonblocking exclusive locks on open files.
// Unix locks cover the whole file. Windows locks cover the first byte.
// Locks remain held until Unlock succeeds or the file is closed.
package filelock
