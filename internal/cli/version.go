package cli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version and exit",
	Run: func(cmd *cobra.Command, args []string) {
		if common.JSON {
			_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"version": Version})
			return
		}
		fmt.Println(Version)
	},
}
