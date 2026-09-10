package main

import (
	"fmt"
	"os"
)

func lockDaemonEndpoint(path string) (*os.File, error) {
	return nil, fmt.Errorf("dynamic daemon ports are supported by the Linux/macOS installer only")
}
