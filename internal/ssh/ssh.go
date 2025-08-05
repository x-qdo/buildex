package ssh

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/x-qdo/buildex/internal/config"
)

// getSSHKeyPath returns the SSH key path, auto-determining it if not explicitly set
func getSSHKeyPath(cfg *config.Config) (string, error) {
	if cfg.SSH.KeyPath != "" {
		return cfg.SSH.KeyPath, nil
	}

	// Auto-determine key path based on AWS key name
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}

	return filepath.Join(homeDir, ".ssh", cfg.AWS.KeyName), nil
}

// Helper manages SSH-related operations
type Helper struct {
	config *config.Config
}

// NewHelper creates a new SSH helper
func NewHelper(cfg *config.Config) *Helper {
	return &Helper{
		config: cfg,
	}
}

// AddHostKey adds a host key to the known_hosts file
func (h *Helper) AddHostKey(ctx context.Context, host string) error {
	if !h.config.SSH.StrictHost {
		// If strict host checking is disabled, no need to add host key
		return nil
	}

	logrus.WithField("host", host).Debug("Adding host key to known_hosts")

	// Get SSH directory
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get home directory: %w", err)
	}

	sshDir := filepath.Join(homeDir, ".ssh")
	if err := os.MkdirAll(sshDir, 0700); err != nil {
		return fmt.Errorf("failed to create .ssh directory: %w", err)
	}

	knownHostsPath := filepath.Join(sshDir, "known_hosts")

	// Check if host key already exists
	if h.hostKeyExists(knownHostsPath, host) {
		logrus.WithField("host", host).Debug("Host key already exists in known_hosts")
		return nil
	}

	// Scan and add the host key
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ssh-keyscan", "-H", host)
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to scan host key for %s: %w", host, err)
	}

	if len(output) == 0 {
		return fmt.Errorf("no host key found for %s", host)
	}

	// Append to known_hosts file
	file, err := os.OpenFile(knownHostsPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("failed to open known_hosts file: %w", err)
	}
	defer file.Close()

	// Add newline if file doesn't end with one
	stat, err := file.Stat()
	if err == nil && stat.Size() > 0 {
		// Check if file ends with newline
		file.Seek(-1, 2)
		lastByte := make([]byte, 1)
		file.Read(lastByte)
		if lastByte[0] != '\n' {
			file.Write([]byte("\n"))
		}
		file.Seek(0, 2) // Go back to end
	}

	if _, err := file.Write(output); err != nil {
		return fmt.Errorf("failed to write host key to known_hosts: %w", err)
	}

	logrus.WithField("host", host).Info("Host key added to known_hosts")
	return nil
}

// RemoveHostKey removes a host key from the known_hosts file
func (h *Helper) RemoveHostKey(host string) error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get home directory: %w", err)
	}

	knownHostsPath := filepath.Join(homeDir, ".ssh", "known_hosts")

	// Check if file exists
	if _, err := os.Stat(knownHostsPath); os.IsNotExist(err) {
		return nil // File doesn't exist, nothing to remove
	}

	// Use ssh-keygen to remove the host key
	cmd := exec.Command("ssh-keygen", "-R", host, "-f", knownHostsPath)
	if err := cmd.Run(); err != nil {
		logrus.WithError(err).WithField("host", host).Warn("Failed to remove host key using ssh-keygen")
		// Fallback to manual removal
		return h.manualRemoveHostKey(knownHostsPath, host)
	}

	logrus.WithField("host", host).Debug("Host key removed from known_hosts")
	return nil
}

// hostKeyExists checks if a host key already exists in known_hosts
func (h *Helper) hostKeyExists(knownHostsPath, host string) bool {
	file, err := os.Open(knownHostsPath)
	if err != nil {
		return false
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, host) {
			return true
		}
	}

	return false
}

// manualRemoveHostKey manually removes host key entries from known_hosts
func (h *Helper) manualRemoveHostKey(knownHostsPath, host string) error {
	file, err := os.Open(knownHostsPath)
	if err != nil {
		return fmt.Errorf("failed to open known_hosts file: %w", err)
	}
	defer file.Close()

	var lines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.Contains(line, host) {
			lines = append(lines, line)
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("failed to read known_hosts file: %w", err)
	}

	// Write back the filtered content
	tempFile := knownHostsPath + ".tmp"
	outFile, err := os.OpenFile(tempFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}

	for _, line := range lines {
		if _, err := fmt.Fprintln(outFile, line); err != nil {
			outFile.Close()
			os.Remove(tempFile)
			return fmt.Errorf("failed to write to temp file: %w", err)
		}
	}

	outFile.Close()

	// Replace original file with temp file
	if err := os.Rename(tempFile, knownHostsPath); err != nil {
		os.Remove(tempFile)
		return fmt.Errorf("failed to replace known_hosts file: %w", err)
	}

	return nil
}

// TestConnection tests SSH connectivity to a host
func (h *Helper) TestConnection(ctx context.Context, host string) error {
	ctx, cancel := context.WithTimeout(ctx, h.config.SSH.Timeout)
	defer cancel()

	args := []string{
		"-o", "ConnectTimeout=10",
		"-o", "BatchMode=yes",
		"-o", "PasswordAuthentication=no",
	}

	// Add strict host checking options based on config
	if !h.config.SSH.StrictHost {
		args = append(args, "-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null")
	}

	// Add port if specified
	if h.config.SSH.Port != 0 && h.config.SSH.Port != 22 {
		args = append(args, "-p", fmt.Sprintf("%d", h.config.SSH.Port))
	}

	// Add key path (auto-determine if not specified)
	if keyPath, err := getSSHKeyPath(h.config); err == nil {
		if _, err := os.Stat(keyPath); err == nil {
			args = append(args, "-i", keyPath)
		}
	}

	args = append(args, fmt.Sprintf("%s@%s", h.config.SSH.User, host), "echo", "SSH connection test successful")

	cmd := exec.CommandContext(ctx, "ssh", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("SSH connection failed: %s", string(output))
	}

	return nil
}

// GetSSHArgs returns SSH arguments based on configuration
func (h *Helper) GetSSHArgs() []string {
	var args []string

	// Add port if specified
	if h.config.SSH.Port != 0 && h.config.SSH.Port != 22 {
		args = append(args, "-p", fmt.Sprintf("%d", h.config.SSH.Port))
	}

	// Add key path (auto-determine if not specified)
	if keyPath, err := getSSHKeyPath(h.config); err == nil {
		if _, err := os.Stat(keyPath); err == nil {
			args = append(args, "-i", keyPath)
		}
	}

	// Add strict host checking options based on config
	if !h.config.SSH.StrictHost {
		args = append(args, "-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null")
	}

	// Add connection timeout
	args = append(args, "-o", "ConnectTimeout=30")

	return args
}

// GetSSHOptsString returns SSH options as a string for Docker contexts
func (h *Helper) GetSSHOptsString() string {
	var opts []string

	// Add port if specified
	if h.config.SSH.Port != 0 && h.config.SSH.Port != 22 {
		opts = append(opts, fmt.Sprintf("-p %d", h.config.SSH.Port))
	}

	// Add key path (auto-determine if not specified)
	if keyPath, err := getSSHKeyPath(h.config); err == nil {
		if _, err := os.Stat(keyPath); err == nil {
			opts = append(opts, fmt.Sprintf("-i %s", keyPath))
		}
	}

	// Add strict host checking options based on config
	if !h.config.SSH.StrictHost {
		opts = append(opts, "-o StrictHostKeyChecking=no", "-o UserKnownHostsFile=/dev/null")
	}

	// Add connection timeout
	opts = append(opts, "-o ConnectTimeout=30")

	return strings.Join(opts, " ")
}

// CreateSSHConfig creates/updates SSH config for a host
func (h *Helper) CreateSSHConfig(host string) error {
	return h.CreateSSHConfigWithKey(host, "")
}

// CreateSSHConfigWithKey creates/updates SSH config for a host with specific key path
func (h *Helper) CreateSSHConfigWithKey(host string, keyPath string) error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get home directory: %w", err)
	}

	sshDir := filepath.Join(homeDir, ".ssh")
	if err := os.MkdirAll(sshDir, 0700); err != nil {
		return fmt.Errorf("failed to create .ssh directory: %w", err)
	}

	configPath := filepath.Join(sshDir, "config")

	// Read existing config
	var existingConfig string
	if data, err := os.ReadFile(configPath); err == nil {
		existingConfig = string(data)
	}

	// Create host entry
	hostEntry := fmt.Sprintf("\n# BuildEx entry for %s\nHost %s\n", host, host)
	hostEntry += fmt.Sprintf("    HostName %s\n", host)
	hostEntry += fmt.Sprintf("    User %s\n", h.config.SSH.User)

	if h.config.SSH.Port != 0 && h.config.SSH.Port != 22 {
		hostEntry += fmt.Sprintf("    Port %d\n", h.config.SSH.Port)
	}

	// Use provided key path or auto-determine if not specified
	effectiveKeyPath := keyPath
	if effectiveKeyPath == "" {
		if autoKeyPath, err := getSSHKeyPath(h.config); err == nil {
			effectiveKeyPath = autoKeyPath
		}
	}

	// Always include key path if available
	if effectiveKeyPath != "" {
		if _, err := os.Stat(effectiveKeyPath); err == nil {
			hostEntry += fmt.Sprintf("    IdentityFile %s\n", effectiveKeyPath)
			hostEntry += "    IdentitiesOnly yes\n"
		} else {
			logrus.WithField("key_path", effectiveKeyPath).Warn("SSH key file not found, SSH authentication may fail")
		}
	}

	if !h.config.SSH.StrictHost {
		hostEntry += "    StrictHostKeyChecking no\n"
		hostEntry += "    UserKnownHostsFile /dev/null\n"
	}

	hostEntry += "    ConnectTimeout 30\n"
	hostEntry += "    ServerAliveInterval 60\n"
	hostEntry += "    ServerAliveCountMax 3\n"

	// Remove existing entry if present
	lines := strings.Split(existingConfig, "\n")
	var filteredLines []string
	skipUntilNextHost := false

	for _, line := range lines {
		if strings.HasPrefix(line, "# BuildEx entry for "+host) {
			skipUntilNextHost = true
			continue
		}
		if skipUntilNextHost && (strings.HasPrefix(line, "Host ") || strings.HasPrefix(line, "# BuildEx entry for ")) && !strings.Contains(line, host) {
			skipUntilNextHost = false
		}
		if !skipUntilNextHost {
			filteredLines = append(filteredLines, line)
		}
	}

	// Add new entry
	newConfig := strings.Join(filteredLines, "\n") + hostEntry

	// Write config
	if err := os.WriteFile(configPath, []byte(newConfig), 0600); err != nil {
		return fmt.Errorf("failed to write SSH config: %w", err)
	}

	logrus.WithField("host", host).Debug("SSH config updated")
	return nil
}

// RemoveSSHConfig removes SSH config for a host
func (h *Helper) RemoveSSHConfig(host string) error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get home directory: %w", err)
	}

	configPath := filepath.Join(homeDir, ".ssh", "config")

	// Read existing config
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // Config doesn't exist, nothing to remove
		}
		return fmt.Errorf("failed to read SSH config: %w", err)
	}

	// Remove host entry
	lines := strings.Split(string(data), "\n")
	var filteredLines []string
	skipUntilNextHost := false

	for _, line := range lines {
		if strings.HasPrefix(line, "# BuildEx entry for "+host) {
			skipUntilNextHost = true
			continue
		}
		if skipUntilNextHost && (strings.HasPrefix(line, "Host ") || strings.HasPrefix(line, "# BuildEx entry for ")) && !strings.Contains(line, host) {
			skipUntilNextHost = false
		}
		if !skipUntilNextHost {
			filteredLines = append(filteredLines, line)
		}
	}

	// Write updated config
	newConfig := strings.Join(filteredLines, "\n")
	if err := os.WriteFile(configPath, []byte(newConfig), 0600); err != nil {
		return fmt.Errorf("failed to write SSH config: %w", err)
	}

	logrus.WithField("host", host).Debug("SSH config entry removed")
	return nil
}
