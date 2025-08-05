package defaults

import (
	_ "embed"
	"fmt"

	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"
)

//go:embed config.yaml
var defaultConfigYAML []byte

// LoadDefaults loads the embedded default configuration into viper
func LoadDefaults() {
	var defaultConfig map[string]interface{}
	if err := yaml.Unmarshal(defaultConfigYAML, &defaultConfig); err != nil {
		panic(fmt.Sprintf("failed to parse embedded default config: %v", err))
	}

	// Set all defaults from the YAML config
	setDefaultsFromMap("", defaultConfig)
}

// setDefaultsFromMap recursively sets viper defaults from a map
func setDefaultsFromMap(prefix string, m map[string]interface{}) {
	for key, value := range m {
		fullKey := key
		if prefix != "" {
			fullKey = prefix + "." + key
		}

		switch v := value.(type) {
		case map[string]interface{}:
			setDefaultsFromMap(fullKey, v)
		default:
			viper.SetDefault(fullKey, v)
		}
	}
}

// GetDefaultConfigYAML returns the raw embedded YAML content
func GetDefaultConfigYAML() []byte {
	return defaultConfigYAML
}
