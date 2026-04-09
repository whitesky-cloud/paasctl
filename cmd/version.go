package cmd

import "github.com/spf13/cobra"

var Version = "dev"

func init() {
	rootCmd.AddCommand(versionCmd)
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Show the paasctl version",
	Run: func(cmd *cobra.Command, args []string) {
		if outputJSON {
			printJSONOK(map[string]interface{}{
				"version": Version,
			})
			return
		}
		cmd.Println(Version)
	},
}
