package cmd

import (
	"log"
	"os"

	"github.com/joho/godotenv"
	"github.com/spf13/cobra"
)

// cfgFile config file variable.
var cfgFile string

// rootCmd represents the base command when called without any subcommands.
var rootCmd = &cobra.Command{
	Use:   "elchi-backend",
	Short: "elchi backend server",
	Long:  ``,
}

// Execute adds all child commands to the root command and sets flags appropriately.
func Execute() {
	err := rootCmd.Execute()
	if err != nil {
		os.Exit(1)
	}
}

func init() {
	err := godotenv.Load()
	if err != nil {
		log.Println("Could not find env. file, using default values (prod)")
	}

	env := os.Getenv("ENV")
	if env == "local" {
		rootCmd.PersistentFlags().StringVar(&cfgFile, "config", ".configs/config-local.yaml", "config file")
	} else {
		rootCmd.PersistentFlags().StringVar(&cfgFile, "config", ".configs/config-prod.yaml", "config file")
	}
}
