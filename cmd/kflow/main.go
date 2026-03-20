// Package main is the kflow CLI client.
// Usage: kflow <group> <command> [flags]
package main

import (
	"flag"
	"fmt"
	"os"
)

var (
	serverFlag = flag.String("server", "http://localhost:8080", "orchestrator base URL")
	apiKeyFlag = flag.String("api-key", "", "bearer token for API auth")
)

func main() {
	flag.Usage = usage
	flag.Parse()

	args := flag.Args()
	if len(args) < 2 {
		usage()
		os.Exit(1)
	}

	group, cmd := args[0], args[1]
	rest := args[2:]

	switch group {
	case "workflow":
		runWorkflowCmd(cmd, rest)
	case "execution":
		runExecutionCmd(cmd, rest)
	default:
		fmt.Fprintf(os.Stderr, "unknown group %q\n", group)
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `kflow — workflow engine CLI

Usage:
  kflow [--server=URL] [--api-key=KEY] <group> <command> [flags]

Groups and commands:
  workflow register  --file=<graph.json>
  workflow list
  workflow run       --name=<name> [--input=<json>]
  execution get      --id=<id>
  execution list     [--workflow=<name>]

Global flags:
`)
	flag.PrintDefaults()
}
