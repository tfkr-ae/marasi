package main

import "os"

func main() {
	if err := executeCommand(os.Args[1:]); err != nil {
		os.Exit(1)
	}
}
