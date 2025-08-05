package cli

import (
	"fmt"
	"os"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/x-qdo/buildex/internal/defaults"
)

var (
	cfgFile string
	verbose bool
	debug   bool
)

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:   "buildex",
	Short: "Docker buildx workers on EC2 Spot instances",
	Long: `BuildEx is a CLI tool that manages Docker buildx workers on EC2 Spot instances
for on-demand building. It provides seamless experience for spawning EC2 instances,
configuring them as Docker buildx workers, and running multi-architecture builds.

Examples:
  # Build on-demand with spot instance
  buildex build . --spot --platform linux/amd64,linux/arm64

  # Start persistent workers
  buildex start --platform linux/amd64,linux/arm64

  # Check status of workers
  buildex status

  # Stop all workers
  buildex stop --all`,
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		initLogging()
	},
}

// Execute adds all child commands to the root command and sets flags appropriately.
// This is called by main.main(). It only needs to happen once to the rootCmd.
func Execute() error {
	return rootCmd.Execute()
}

func init() {
	cobra.OnInitialize(initConfig)

	// Global flags
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is $HOME/.buildex.yaml)")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "verbose output")
	rootCmd.PersistentFlags().BoolVar(&debug, "debug", false, "debug output")

	// AWS flags
	rootCmd.PersistentFlags().String("region", "us-east-1", "AWS region")
	rootCmd.PersistentFlags().String("profile", "", "AWS profile")

	// Bind flags to viper
	viper.BindPFlag("verbose", rootCmd.PersistentFlags().Lookup("verbose"))
	viper.BindPFlag("debug", rootCmd.PersistentFlags().Lookup("debug"))
	viper.BindPFlag("aws.region", rootCmd.PersistentFlags().Lookup("region"))
	viper.BindPFlag("aws.profile", rootCmd.PersistentFlags().Lookup("profile"))
}

// initConfig reads in config file and ENV variables if set.
func initConfig() {
	if cfgFile != "" {
		// Use config file from the flag.
		viper.SetConfigFile(cfgFile)
	} else {
		// Find home directory.
		home, err := os.UserHomeDir()
		cobra.CheckErr(err)

		// Search for .buildex.yaml in home directory and current directory
		viper.AddConfigPath(home)
		viper.AddConfigPath(".")
		viper.SetConfigType("yaml")
		viper.SetConfigName(".buildex")
	}

	// Environment variables
	viper.SetEnvPrefix("BUILDEX")
	viper.AutomaticEnv()

	// Set defaults
	setDefaults()

	// If a config file is found, read it in.
	if err := viper.ReadInConfig(); err == nil {
		if viper.GetBool("verbose") {
			fmt.Fprintln(os.Stderr, "Using config file:", viper.ConfigFileUsed())
		}
	}
}

func setDefaults() {
	// Load defaults from embedded YAML
	defaults.LoadDefaults()
}

func initLogging() {
	// Set log level
	if viper.GetBool("debug") {
		logrus.SetLevel(logrus.DebugLevel)
	} else if viper.GetBool("verbose") {
		logrus.SetLevel(logrus.InfoLevel)
	} else {
		logrus.SetLevel(logrus.WarnLevel)
	}

	// Set log format
	logrus.SetFormatter(&logrus.TextFormatter{
		DisableColors:   false,
		FullTimestamp:   true,
		TimestampFormat: "2006-01-02 15:04:05",
	})
}
