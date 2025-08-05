package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// configCmd represents the config command
var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Print current configuration values",
	Long: `Print all current configuration values loaded by viper.
This includes values from config files, environment variables, and defaults.
Useful for debugging configuration issues.

Examples:
  # Show all configuration values
  buildex config

  # Show configuration with verbose output
  buildex config --verbose`,
	Run: func(cmd *cobra.Command, args []string) {
		printConfig()
	},
}

func init() {
	rootCmd.AddCommand(configCmd)
}

func printConfig() {
	fmt.Println("BuildEx Configuration")
	fmt.Println("====================")

	// Show config file being used (if any)
	configFile := viper.ConfigFileUsed()
	if configFile != "" {
		fmt.Printf("Config file: %s\n", configFile)
	} else {
		fmt.Println("Config file: None (using defaults and environment variables)")
	}
	fmt.Println()

	// Get all settings
	settings := viper.AllSettings()

	if len(settings) == 0 {
		fmt.Println("No configuration values found.")
		return
	}

	// Print settings in a structured way
	printSettingsRecursive("", settings, 0)
}

func printSettingsRecursive(prefix string, settings map[string]interface{}, indent int) {
	// Sort keys for consistent output
	keys := make([]string, 0, len(settings))
	for k := range settings {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	indentStr := strings.Repeat("  ", indent)

	for _, key := range keys {
		value := settings[key]
		fullKey := key
		if prefix != "" {
			fullKey = prefix + "." + key
		}

		switch v := value.(type) {
		case map[string]interface{}:
			fmt.Printf("%s%s:\n", indentStr, key)
			printSettingsRecursive(fullKey, v, indent+1)
		case []interface{}:
			fmt.Printf("%s%s: [", indentStr, key)
			for i, item := range v {
				if i > 0 {
					fmt.Print(", ")
				}
				fmt.Printf("%v", item)
			}
			fmt.Println("]")
		default:
			fmt.Printf("%s%s: %v\n", indentStr, key, v)
		}
	}
}
