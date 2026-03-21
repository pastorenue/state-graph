package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var workflowCmd = &cobra.Command{
	Use:   "workflow",
	Short: "Manage workflows",
}

var workflowRegisterCmd = &cobra.Command{
	Use:   "register",
	Short: "Register a workflow from a graph JSON file",
	RunE: func(cmd *cobra.Command, args []string) error {
		file, _ := cmd.Flags().GetString("file")
		raw, err := os.ReadFile(file)
		if err != nil {
			return fmt.Errorf("reading file: %w", err)
		}
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			return fmt.Errorf("parsing JSON: %w", err)
		}
		result, err := doJSON("POST", "/api/v1/workflows", body)
		if err != nil {
			return err
		}
		printJSON(result)
		return nil
	},
}

var workflowListCmd = &cobra.Command{
	Use:   "list",
	Short: "List registered workflows",
	RunE: func(cmd *cobra.Command, args []string) error {
		result, err := doJSON("GET", "/api/v1/workflows", nil)
		if err != nil {
			return err
		}
		printJSON(result)
		return nil
	},
}

var workflowRunCmd = &cobra.Command{
	Use:   "run",
	Short: "Trigger a workflow execution",
	RunE: func(cmd *cobra.Command, args []string) error {
		name, _ := cmd.Flags().GetString("name")
		inputJSON, _ := cmd.Flags().GetString("input")
		var input map[string]any
		if err := json.Unmarshal([]byte(inputJSON), &input); err != nil {
			return fmt.Errorf("parsing --input JSON: %w", err)
		}
		result, err := doJSON("POST", "/api/v1/workflows/"+name+"/run", map[string]any{"input": input})
		if err != nil {
			return err
		}
		printJSON(result)
		return nil
	},
}

func init() {
	workflowRegisterCmd.Flags().String("file", "", "path to workflow graph JSON file")
	_ = workflowRegisterCmd.MarkFlagRequired("file")

	workflowRunCmd.Flags().String("name", "", "workflow name")
	workflowRunCmd.Flags().String("input", "{}", "workflow input as JSON object")
	_ = workflowRunCmd.MarkFlagRequired("name")

	workflowCmd.AddCommand(workflowRegisterCmd, workflowListCmd, workflowRunCmd)
}
