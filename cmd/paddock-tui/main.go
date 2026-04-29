/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// paddock-tui is the interactive multi-session TUI for Paddock.
// See internal/paddocktui for command and TUI implementations.
package main

import (
	"fmt"
	"os"

	"paddock.dev/paddock/internal/paddocktui/cmd"
)

func main() {
	if err := cmd.NewRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "paddock-tui:", err)
		os.Exit(1)
	}
}
