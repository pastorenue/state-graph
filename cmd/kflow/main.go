// Package main is the kflow CLI client.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var (
	serverFlag string
	apiKeyFlag string
)

var rootCmd = &cobra.Command{
	Use:   "kflow",
	Short: "kflow — workflow engine CLI",
}

func init() {
	rootCmd.PersistentFlags().StringVar(&serverFlag, "server", "http://localhost:8080", "orchestrator base URL")
	rootCmd.PersistentFlags().StringVar(&apiKeyFlag, "api-key", "", "bearer token for API auth")
	rootCmd.AddCommand(workflowCmd)
	rootCmd.AddCommand(executionCmd)
	rootCmd.AddCommand(serviceCmd)
	rootCmd.AddCommand(authCmd)
	rootCmd.AddCommand(uiCmd)
	rootCmd.AddCommand(logsCmd)
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
