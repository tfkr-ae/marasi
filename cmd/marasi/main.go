// Command marasi starts and stops named proxy instances and inspects
// captured HTTP traffic through a Unix-socket control API.
package main

import "os"

var version = "dev"

func main() {
	if err := executeCommand(os.Args[1:]); err != nil {
		os.Exit(1)
	}
}
