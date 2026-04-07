package cmd

import "github.com/spf13/cobra"

func init() {
	rootCmd.AddCommand(addCmd)
}

var addCmd = &cobra.Command{
	Use:   "add",
	Short: "Add resources to an existing deployment",
}
