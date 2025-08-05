package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"
)

// Config represents the application configuration
type Config struct {
	AWS      AWSConfig     `yaml:"aws" mapstructure:"aws"`
	Buildx   BuildxConfig  `yaml:"buildx" mapstructure:"buildx"`
	UI       UIConfig      `yaml:"ui" mapstructure:"ui"`
	Timeouts TimeoutConfig `yaml:"timeouts" mapstructure:"timeouts"`
	SSH      SSHConfig     `yaml:"ssh" mapstructure:"ssh"`
	State    StateConfig   `yaml:"state" mapstructure:"state"`
}

// AWSConfig holds AWS-related configuration
type AWSConfig struct {
	Region        string `yaml:"region" mapstructure:"region"`
	Profile       string `yaml:"profile" mapstructure:"profile"`
	InstanceType  string `yaml:"instance_type" mapstructure:"instance_type"`
	KeyName       string `yaml:"key_name" mapstructure:"key_name"`
	SecurityGroup string `yaml:"security_group" mapstructure:"security_group"`
	UseSpot       bool   `yaml:"use_spot" mapstructure:"use_spot"`

	AMI        string                     `yaml:"ami" mapstructure:"ami"`
	VolumeSize int                        `yaml:"volume_size" mapstructure:"volume_size"`
	SubnetID   string                     `yaml:"subnet_id" mapstructure:"subnet_id"`
	Tags       map[string]string          `yaml:"tags" mapstructure:"tags"`
	Platforms  map[string]*PlatformConfig `yaml:"platforms" mapstructure:"platforms"`
}

// PlatformConfig holds platform-specific AWS configuration
type PlatformConfig struct {
	InstanceType string `yaml:"instance_type" mapstructure:"instance_type"`
	UseSpot      *bool  `yaml:"use_spot" mapstructure:"use_spot"`

	AMI        string `yaml:"ami" mapstructure:"ami"`
	VolumeSize *int   `yaml:"volume_size" mapstructure:"volume_size"`
}

// BuildxConfig holds Docker buildx configuration
type BuildxConfig struct {
	BuilderName string   `yaml:"builder_name" mapstructure:"builder_name"`
	Platforms   []string `yaml:"platforms" mapstructure:"platforms"`
	DriverOpts  []string `yaml:"driver_opts" mapstructure:"driver_opts"`
	BuildArgs   []string `yaml:"build_args" mapstructure:"build_args"`
	Push        bool     `yaml:"push" mapstructure:"push"`
	Load        bool     `yaml:"load" mapstructure:"load"`
}

// UIConfig holds user interface configuration
type UIConfig struct {
	SplitScreen    bool `yaml:"split_screen" mapstructure:"split_screen"`
	ShowTimestamps bool `yaml:"show_timestamps" mapstructure:"show_timestamps"`
	Colors         bool `yaml:"colors" mapstructure:"colors"`
	Spinner        bool `yaml:"spinner" mapstructure:"spinner"`
}

// TimeoutConfig holds timeout configuration
type TimeoutConfig struct {
	InstanceStartup time.Duration `yaml:"instance_startup" mapstructure:"instance_startup"`
	DockerReady     time.Duration `yaml:"docker_ready" mapstructure:"docker_ready"`
	BuildTimeout    time.Duration `yaml:"build_timeout" mapstructure:"build_timeout"`
	SSHConnect      time.Duration `yaml:"ssh_connect" mapstructure:"ssh_connect"`
}

// SSHConfig holds SSH configuration
type SSHConfig struct {
	User       string        `yaml:"user" mapstructure:"user"`
	Port       int           `yaml:"port" mapstructure:"port"`
	Timeout    time.Duration `yaml:"timeout" mapstructure:"timeout"`
	KeyPath    string        `yaml:"key_path" mapstructure:"key_path"`
	StrictHost bool          `yaml:"strict_host" mapstructure:"strict_host"`
}

// StateConfig holds state management configuration
type StateConfig struct {
	Dir      string `yaml:"dir" mapstructure:"dir"`
	FileName string `yaml:"file_name" mapstructure:"file_name"`
	AutoSave bool   `yaml:"auto_save" mapstructure:"auto_save"`
}

// WorkerState represents the state of a buildx worker
type WorkerState struct {
	Name         string            `yaml:"name" json:"name"`
	InstanceID   string            `yaml:"instance_id" json:"instance_id"`
	PublicIP     string            `yaml:"public_ip" json:"public_ip"`
	PrivateIP    string            `yaml:"private_ip" json:"private_ip"`
	Platform     string            `yaml:"platform" json:"platform"`
	InstanceType string            `yaml:"instance_type" json:"instance_type"`
	Region       string            `yaml:"region" json:"region"`
	IsSpot       bool              `yaml:"is_spot" json:"is_spot"`
	Status       string            `yaml:"status" json:"status"`
	CreatedAt    time.Time         `yaml:"created_at" json:"created_at"`
	UpdatedAt    time.Time         `yaml:"updated_at" json:"updated_at"`
	Tags         map[string]string `yaml:"tags" json:"tags"`
	SSHKeyPath   string            `yaml:"ssh_key_path" json:"ssh_key_path"`
	BuilderName  string            `yaml:"builder_name" json:"builder_name"`
}

// GlobalState represents the global state of all workers
type GlobalState struct {
	Workers   map[string]*WorkerState `yaml:"workers" json:"workers"`
	Builders  map[string][]string     `yaml:"builders" json:"builders"`
	UpdatedAt time.Time               `yaml:"updated_at" json:"updated_at"`
}

var (
	globalConfig *Config
	globalState  *GlobalState
)

// Load loads the configuration from viper
func Load() (*Config, error) {
	if globalConfig != nil {
		return globalConfig, nil
	}

	config := &Config{}
	if err := viper.Unmarshal(config); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	// Ensure state directory exists
	if config.State.Dir == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to get home directory: %w", err)
		}
		config.State.Dir = filepath.Join(homeDir, ".buildex")
	}

	if err := os.MkdirAll(config.State.Dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create state directory: %w", err)
	}

	globalConfig = config
	return config, nil
}

// Get returns the current configuration
func Get() *Config {
	if globalConfig == nil {
		config, err := Load()
		if err != nil {
			panic(fmt.Sprintf("failed to load config: %v", err))
		}
		return config
	}
	return globalConfig
}

// LoadState loads the worker state from disk
func LoadState() (*GlobalState, error) {
	if globalState != nil {
		return globalState, nil
	}

	config := Get()
	stateFile := filepath.Join(config.State.Dir, config.State.FileName)
	if config.State.FileName == "" {
		stateFile = filepath.Join(config.State.Dir, "state.yaml")
	}

	state := &GlobalState{
		Workers:  make(map[string]*WorkerState),
		Builders: make(map[string][]string),
	}

	// If state file doesn't exist, return empty state
	if _, err := os.Stat(stateFile); os.IsNotExist(err) {
		globalState = state
		return state, nil
	}

	data, err := os.ReadFile(stateFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read state file: %w", err)
	}

	if err := yaml.Unmarshal(data, state); err != nil {
		return nil, fmt.Errorf("failed to unmarshal state: %w", err)
	}

	globalState = state
	return state, nil
}

// SaveState saves the worker state to disk
func SaveState(state *GlobalState) error {
	config := Get()
	stateFile := filepath.Join(config.State.Dir, config.State.FileName)
	if config.State.FileName == "" {
		stateFile = filepath.Join(config.State.Dir, "state.yaml")
	}

	state.UpdatedAt = time.Now()

	data, err := yaml.Marshal(state)
	if err != nil {
		return fmt.Errorf("failed to marshal state: %w", err)
	}

	if err := os.WriteFile(stateFile, data, 0644); err != nil {
		return fmt.Errorf("failed to write state file: %w", err)
	}

	globalState = state
	return nil
}

// GetState returns the current global state
func GetState() *GlobalState {
	if globalState == nil {
		state, err := LoadState()
		if err != nil {
			panic(fmt.Sprintf("failed to load state: %v", err))
		}
		return state
	}
	return globalState
}

// AddWorker adds a worker to the state
func (s *GlobalState) AddWorker(worker *WorkerState) {
	if s.Workers == nil {
		s.Workers = make(map[string]*WorkerState)
	}
	worker.UpdatedAt = time.Now()
	s.Workers[worker.Name] = worker
}

// RemoveWorker removes a worker from the state
func (s *GlobalState) RemoveWorker(name string) {
	if s.Workers != nil {
		delete(s.Workers, name)
	}
}

// GetWorker returns a worker by name
func (s *GlobalState) GetWorker(name string) (*WorkerState, bool) {
	if s.Workers == nil {
		return nil, false
	}
	worker, exists := s.Workers[name]
	return worker, exists
}

// ListWorkers returns all workers
func (s *GlobalState) ListWorkers() []*WorkerState {
	var workers []*WorkerState
	for _, worker := range s.Workers {
		workers = append(workers, worker)
	}
	return workers
}

// AddBuilder adds a builder with its workers
func (s *GlobalState) AddBuilder(name string, workers []string) {
	if s.Builders == nil {
		s.Builders = make(map[string][]string)
	}
	s.Builders[name] = workers
}

// RemoveBuilder removes a builder
func (s *GlobalState) RemoveBuilder(name string) {
	if s.Builders != nil {
		delete(s.Builders, name)
	}
}

// GetBuilder returns workers for a builder
func (s *GlobalState) GetBuilder(name string) ([]string, bool) {
	if s.Builders == nil {
		return nil, false
	}
	workers, exists := s.Builders[name]
	return workers, exists
}

// IsPermanentWorker checks if a worker is permanent based on its name pattern
func (w *WorkerState) IsPermanentWorker() bool {
	return strings.HasPrefix(w.Name, "buildex-") && !strings.HasPrefix(w.Name, "build-")
}

// IsTemporaryWorker checks if a worker is temporary based on its name pattern
func (w *WorkerState) IsTemporaryWorker() bool {
	return strings.HasPrefix(w.Name, "build-")
}

// GetPermanentBuilderName returns the consistent builder name for permanent workers
func GetPermanentBuilderName() string {
	return "buildex-permanent"
}

// GenerateTemporaryBuilderName generates a unique builder name for temporary workers
func GenerateTemporaryBuilderName() string {
	return fmt.Sprintf("buildex-build-%d", time.Now().Unix())
}

// GenerateTemporaryWorkerName generates a temporary worker name
func GenerateTemporaryWorkerName(buildID, platform string) string {
	return fmt.Sprintf("build-%s-%s", buildID, SanitizePlatform(platform))
}

// GeneratePermanentWorkerName generates a permanent worker name
func GeneratePermanentWorkerName(baseName, platform string) string {
	return fmt.Sprintf("buildex-%s-%s", baseName, SanitizePlatform(platform))
}

// IsPermanentBuilder checks if a builder name is for permanent workers
func IsPermanentBuilder(builderName string) bool {
	return builderName == GetPermanentBuilderName()
}

// IsTemporaryBuilder checks if a builder name is for temporary workers
func IsTemporaryBuilder(builderName string) bool {
	return strings.HasPrefix(builderName, "buildex-build-")
}

// GetWorkersByBuilder returns all workers that belong to a specific builder
func (s *GlobalState) GetWorkersByBuilder(builderName string) []*WorkerState {
	var workers []*WorkerState
	for _, worker := range s.Workers {
		if worker.BuilderName == builderName {
			workers = append(workers, worker)
		}
	}
	return workers
}

// GetPermanentWorkers returns all permanent workers
func (s *GlobalState) GetPermanentWorkers() []*WorkerState {
	var workers []*WorkerState
	for _, worker := range s.Workers {
		if worker.IsPermanentWorker() {
			workers = append(workers, worker)
		}
	}
	return workers
}

// GetTemporaryWorkers returns all temporary workers
func (s *GlobalState) GetTemporaryWorkers() []*WorkerState {
	var workers []*WorkerState
	for _, worker := range s.Workers {
		if worker.IsTemporaryWorker() {
			workers = append(workers, worker)
		}
	}
	return workers
}

// CleanupTemporaryWorkers removes all temporary workers from state
func (s *GlobalState) CleanupTemporaryWorkers() {
	if s.Workers == nil {
		return
	}

	for name, worker := range s.Workers {
		if worker.IsTemporaryWorker() {
			delete(s.Workers, name)
		}
	}
}

// SanitizePlatform converts platform string to safe filename
func SanitizePlatform(platform string) string {
	return strings.ReplaceAll(strings.ReplaceAll(platform, "/", "-"), ":", "-")
}

// Validate validates the configuration
func (c *Config) Validate() error {
	if c.AWS.Region == "" {
		return fmt.Errorf("aws.region is required")
	}

	if c.AWS.InstanceType == "" {
		return fmt.Errorf("aws.instance_type is required")
	}

	if c.SSH.User == "" {
		return fmt.Errorf("ssh.user is required")
	}

	if c.SSH.Port <= 0 || c.SSH.Port > 65535 {
		return fmt.Errorf("ssh.port must be between 1 and 65535")
	}

	if len(c.Buildx.Platforms) == 0 {
		return fmt.Errorf("buildx.platforms cannot be empty")
	}

	return nil
}

// GetInstanceTypeForPlatform returns the instance type for a specific platform
func (c *Config) GetInstanceTypeForPlatform(platform string) string {
	if platformConfig, exists := c.AWS.Platforms[platform]; exists && platformConfig.InstanceType != "" {
		return platformConfig.InstanceType
	}
	return c.AWS.InstanceType
}

// GetUseSpotForPlatform returns whether to use spot instances for a specific platform
func (c *Config) GetUseSpotForPlatform(platform string) bool {
	if platformConfig, exists := c.AWS.Platforms[platform]; exists && platformConfig.UseSpot != nil {
		return *platformConfig.UseSpot
	}
	return c.AWS.UseSpot
}

// GetAMIForPlatform returns the AMI for a specific platform
func (c *Config) GetAMIForPlatform(platform string) string {
	if platformConfig, exists := c.AWS.Platforms[platform]; exists && platformConfig.AMI != "" {
		return platformConfig.AMI
	}
	return c.AWS.AMI
}

// GetVolumeSizeForPlatform returns the volume size for a specific platform
func (c *Config) GetVolumeSizeForPlatform(platform string) int {
	if platformConfig, exists := c.AWS.Platforms[platform]; exists && platformConfig.VolumeSize != nil {
		return *platformConfig.VolumeSize
	}
	return c.AWS.VolumeSize
}

// GetConfigPath returns the path to the configuration file
func GetConfigPath() string {
	if viper.ConfigFileUsed() != "" {
		return viper.ConfigFileUsed()
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ".buildex.yaml"
	}

	return filepath.Join(homeDir, ".buildex.yaml")
}

// SaveConfig saves the current configuration to file
func SaveConfig(config *Config) error {
	configPath := GetConfigPath()

	data, err := yaml.Marshal(config)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	if err := os.WriteFile(configPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	return nil
}
