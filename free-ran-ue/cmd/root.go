package cmd

import (
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "free-ran-ue",
	Short: "This is a gNB and UE simulator.",
	Long:  "This is a gNB and UE simulator for NR-DC feature in free5GC.",
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
