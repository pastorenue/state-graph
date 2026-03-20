package main

import (
	"flag"
	"fmt"
	"os"
)

func runExecutionCmd(cmd string, args []string) {
	switch cmd {
	case "get":
		executionGet(args)
	case "list":
		executionList(args)
	default:
		fmt.Fprintf(os.Stderr, "unknown execution command %q\n", cmd)
		os.Exit(1)
	}
}

func executionGet(args []string) {
	fs := flag.NewFlagSet("execution get", flag.ExitOnError)
	id := fs.String("id", "", "execution ID (required)")
	_ = fs.Parse(args)

	if *id == "" {
		fmt.Fprintln(os.Stderr, "error: --id is required")
		os.Exit(1)
	}

	result, err := doJSON("GET", "/api/v1/executions/"+*id, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	printJSON(result)
}

func executionList(args []string) {
	fs := flag.NewFlagSet("execution list", flag.ExitOnError)
	workflow := fs.String("workflow", "", "filter by workflow name")
	_ = fs.Parse(args)

	path := "/api/v1/executions"
	if *workflow != "" {
		path += "?workflow=" + *workflow
	}

	result, err := doJSON("GET", path, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	printJSON(result)
}
