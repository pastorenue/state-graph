package main

import (
	"github.com/spf13/cobra"
)

var executionCmd = &cobra.Command{
	Use:   "execution",
	Short: "Manage executions",
}

var executionGetCmd = &cobra.Command{
	Use:   "get",
	Short: "Get a single execution by ID",
	RunE: func(cmd *cobra.Command, args []string) error {
		id, _ := cmd.Flags().GetString("id")
		result, err := doJSON("GET", "/api/v1/executions/"+id, nil)
		if err != nil {
			return err
		}
		printJSON(result)
		return nil
	},
}

var executionListCmd = &cobra.Command{
	Use:   "list",
	Short: "List executions",
	RunE: func(cmd *cobra.Command, args []string) error {
		workflow, _ := cmd.Flags().GetString("workflow")
		path := "/api/v1/executions"
		if workflow != "" {
			path += "?workflow=" + workflow
		}
		result, err := doJSON("GET", path, nil)
		if err != nil {
			return err
		}
		printJSON(result)
		return nil
	},
}

func init() {
	executionGetCmd.Flags().String("id", "", "execution ID")
	_ = executionGetCmd.MarkFlagRequired("id")

	executionListCmd.Flags().String("workflow", "", "filter by workflow name")

	executionCmd.AddCommand(executionGetCmd, executionListCmd)
}
