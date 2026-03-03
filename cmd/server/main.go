package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] != "serve" {
		fmt.Fprintln(os.Stderr, "usage: cgm-get-agent serve")
		os.Exit(1)
	}
	// Full server implementation added in feat/mcp-server phase.
	fmt.Fprintln(os.Stderr, "cgm-get-agent: server not yet implemented")
	os.Exit(1)
}
