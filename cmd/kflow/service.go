package main

import "github.com/spf13/cobra"

var serviceCmd = &cobra.Command{
	Use:   "service",
	Short: "Manage services",
}

var serviceListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all services",
	RunE: func(cmd *cobra.Command, args []string) error {
		result, err := doJSON("GET", "/api/v1/services", nil)
		if err != nil {
			return err
		}
		printJSON(result)
		return nil
	},
}

var serviceGetCmd = &cobra.Command{
	Use:   "get",
	Short: "Get a service by name",
	RunE: func(cmd *cobra.Command, args []string) error {
		name, _ := cmd.Flags().GetString("name")
		result, err := doJSON("GET", "/api/v1/services/"+name, nil)
		if err != nil {
			return err
		}
		printJSON(result)
		return nil
	},
}

var serviceDeleteCmd = &cobra.Command{
	Use:   "delete",
	Short: "Delete a service by name",
	RunE: func(cmd *cobra.Command, args []string) error {
		name, _ := cmd.Flags().GetString("name")
		result, err := doJSON("DELETE", "/api/v1/services/"+name, nil)
		if err != nil {
			return err
		}
		printJSON(result)
		return nil
	},
}

func init() {
	serviceGetCmd.Flags().String("name", "", "service name")
	_ = serviceGetCmd.MarkFlagRequired("name")

	serviceDeleteCmd.Flags().String("name", "", "service name")
	_ = serviceDeleteCmd.MarkFlagRequired("name")

	serviceCmd.AddCommand(serviceListCmd, serviceGetCmd, serviceDeleteCmd)
}
