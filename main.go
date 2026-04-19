package main

import (
	"fmt"
	"os"
	"strings"
)

const version = "0.0.1-prereq"

func main() {
	if len(os.Args) < 2 {
		fmt.Printf("treadle v%s (prereq scratch)\nUsage: treadle <cmd> [args...]\n", version)
		os.Exit(0)
	}
	cmd := os.Args[1]
	switch cmd {
	case "--version", "version", "-v":
		fmt.Printf("treadle v%s\n", version)
	case "hello":
		name := "world"
		if len(os.Args) > 2 {
			name = strings.Join(os.Args[2:], " ")
		}
		fmt.Printf("treadle says hello to %s (from real Go binary v%s)\n", name, version)
	default:
		fmt.Fprintf(os.Stderr, "treadle: unknown command %q\n", cmd)
		os.Exit(2)
	}
}
