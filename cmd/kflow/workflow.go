package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
)

func runWorkflowCmd(cmd string, args []string) {
	switch cmd {
	case "register":
		workflowRegister(args)
	case "list":
		workflowList(args)
	case "run":
		workflowRun(args)
	default:
		fmt.Fprintf(os.Stderr, "unknown workflow command %q\n", cmd)
		os.Exit(1)
	}
}

func workflowRegister(args []string) {
	fs := flag.NewFlagSet("workflow register", flag.ExitOnError)
	file := fs.String("file", "", "path to workflow graph JSON file (required)")
	_ = fs.Parse(args)

	if *file == "" {
		fmt.Fprintln(os.Stderr, "error: --file is required")
		os.Exit(1)
	}

	raw, err := os.ReadFile(*file)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error reading file: %v\n", err)
		os.Exit(1)
	}

	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		fmt.Fprintf(os.Stderr, "error parsing JSON: %v\n", err)
		os.Exit(1)
	}

	result, err := doJSON("POST", "/api/v1/workflows", body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	printJSON(result)
}

func workflowList(args []string) {
	fs := flag.NewFlagSet("workflow list", flag.ExitOnError)
	_ = fs.Parse(args)

	result, err := doJSON("GET", "/api/v1/workflows", nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	printJSON(result)
}

func workflowRun(args []string) {
	fs := flag.NewFlagSet("workflow run", flag.ExitOnError)
	name := fs.String("name", "", "workflow name (required)")
	inputJSON := fs.String("input", "{}", "workflow input as JSON object")
	_ = fs.Parse(args)

	if *name == "" {
		fmt.Fprintln(os.Stderr, "error: --name is required")
		os.Exit(1)
	}

	var input map[string]any
	if err := json.Unmarshal([]byte(*inputJSON), &input); err != nil {
		fmt.Fprintf(os.Stderr, "error parsing --input JSON: %v\n", err)
		os.Exit(1)
	}

	result, err := doJSON("POST", "/api/v1/workflows/"+*name+"/run", map[string]any{"input": input})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	printJSON(result)
}
