package ssh

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/x-qdo/buildex/internal/config"
)

func TestNewHelper(t *testing.T) {
	cfg := &config.Config{
		SSH: config.SSHConfig{
			User:       "testuser",
			Port:       22,
			Timeout:    30 * time.Second,
			StrictHost: false,
		},
	}

	helper := NewHelper(cfg)
	if helper == nil {
		t.Fatal("NewHelper returned nil")
	}
	if helper.config != cfg {
		t.Error("Helper config not set correctly")
	}
}

func TestGetSSHArgs(t *testing.T) {
	// Create temporary directory for testing
	tempDir := t.TempDir()

	// Mock home directory
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempDir)
	defer os.Setenv("HOME", originalHome)

	// Create mock key file
	keyPath := filepath.Join(tempDir, "test-key")
	err := os.WriteFile(keyPath, []byte("mock key content"), 0600)
	if err != nil {
		t.Fatalf("Failed to create mock key file: %v", err)
	}

	tests := []struct {
		name     string
		config   config.SSHConfig
		expected []string
	}{
		{
			name: "default config",
			config: config.SSHConfig{
				User:       "ec2-user",
				Port:       22,
				StrictHost: false,
			},
			expected: []string{
				"-o", "StrictHostKeyChecking=no",
				"-o", "UserKnownHostsFile=/dev/null",
				"-o", "ConnectTimeout=30",
			},
		},
		{
			name: "custom port and key",
			config: config.SSHConfig{
				User:       "ubuntu",
				Port:       2222,
				KeyPath:    keyPath,
				StrictHost: true,
			},
			expected: []string{
				"-p", "2222",
				"-i", keyPath,
				"-o", "ConnectTimeout=30",
			},
		},
		{
			name: "strict host checking disabled",
			config: config.SSHConfig{
				User:       "ec2-user",
				Port:       22,
				StrictHost: false,
			},
			expected: []string{
				"-o", "StrictHostKeyChecking=no",
				"-o", "UserKnownHostsFile=/dev/null",
				"-o", "ConnectTimeout=30",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{SSH: tt.config}
			helper := NewHelper(cfg)

			args := helper.GetSSHArgs()

			// Check that all expected args are present
			for _, expected := range tt.expected {
				found := false
				for _, arg := range args {
					if arg == expected {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("Expected argument %q not found in %v", expected, args)
				}
			}
		})
	}
}

func TestGetSSHOptsString(t *testing.T) {
	// Create temporary directory for testing
	tempDir := t.TempDir()

	// Mock home directory
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempDir)
	defer os.Setenv("HOME", originalHome)

	// Create mock key file
	keyPath := filepath.Join(tempDir, "test-key")
	err := os.WriteFile(keyPath, []byte("mock key content"), 0600)
	if err != nil {
		t.Fatalf("Failed to create mock key file: %v", err)
	}

	tests := []struct {
		name     string
		config   config.SSHConfig
		contains []string
	}{
		{
			name: "strict host disabled",
			config: config.SSHConfig{
				User:       "ec2-user",
				StrictHost: false,
			},
			contains: []string{
				"-o StrictHostKeyChecking=no",
				"-o UserKnownHostsFile=/dev/null",
				"-o ConnectTimeout=30",
			},
		},
		{
			name: "custom port and key",
			config: config.SSHConfig{
				Port:       2222,
				KeyPath:    keyPath,
				StrictHost: true,
			},
			contains: []string{
				"-p 2222",
				"-i " + keyPath,
				"-o ConnectTimeout=30",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{SSH: tt.config}
			helper := NewHelper(cfg)

			opts := helper.GetSSHOptsString()

			for _, expected := range tt.contains {
				if !strings.Contains(opts, expected) {
					t.Errorf("Expected %q to contain %q", opts, expected)
				}
			}
		})
	}
}

func TestCreateSSHConfig(t *testing.T) {
	// Create temporary directory for testing
	tempDir := t.TempDir()

	// Mock home directory
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempDir)
	defer os.Setenv("HOME", originalHome)

	// Create mock key file
	keyPath := filepath.Join(tempDir, "test-key")
	err := os.WriteFile(keyPath, []byte("mock key content"), 0600)
	if err != nil {
		t.Fatalf("Failed to create mock key file: %v", err)
	}

	cfg := &config.Config{
		SSH: config.SSHConfig{
			User:       "testuser",
			Port:       2222,
			KeyPath:    keyPath,
			StrictHost: false,
		},
	}

	helper := NewHelper(cfg)
	host := "192.168.1.100"

	err = helper.CreateSSHConfig(host)
	if err != nil {
		t.Fatalf("CreateSSHConfig failed: %v", err)
	}

	// Check that config file was created
	configPath := filepath.Join(tempDir, ".ssh", "config")
	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("Failed to read SSH config: %v", err)
	}

	configStr := string(content)

	// Check that config contains expected entries
	expectedEntries := []string{
		"# BuildEx entry for " + host,
		"Host " + host,
		"HostName " + host,
		"User testuser",
		"Port 2222",
		"IdentityFile " + keyPath,
		"IdentitiesOnly yes",
		"StrictHostKeyChecking no",
		"UserKnownHostsFile /dev/null",
		"ConnectTimeout 30",
		"ServerAliveInterval 60",
		"ServerAliveCountMax 3",
	}

	for _, expected := range expectedEntries {
		if !strings.Contains(configStr, expected) {
			t.Errorf("SSH config missing expected entry: %q\nConfig content:\n%s", expected, configStr)
		}
	}
}

func TestRemoveSSHConfig(t *testing.T) {
	// Create temporary directory for testing
	tempDir := t.TempDir()

	// Mock home directory
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempDir)
	defer os.Setenv("HOME", originalHome)

	cfg := &config.Config{
		SSH: config.SSHConfig{
			User:       "testuser",
			StrictHost: false,
		},
	}

	helper := NewHelper(cfg)
	host1 := "192.168.1.100"
	host2 := "192.168.1.101"

	// Create SSH config with two hosts
	err := helper.CreateSSHConfig(host1)
	if err != nil {
		t.Fatalf("CreateSSHConfig failed: %v", err)
	}

	err = helper.CreateSSHConfig(host2)
	if err != nil {
		t.Fatalf("CreateSSHConfig failed: %v", err)
	}

	// Remove first host
	err = helper.RemoveSSHConfig(host1)
	if err != nil {
		t.Fatalf("RemoveSSHConfig failed: %v", err)
	}

	// Check that first host is removed but second remains
	configPath := filepath.Join(tempDir, ".ssh", "config")
	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("Failed to read SSH config: %v", err)
	}

	configStr := string(content)

	// First host should be gone
	if strings.Contains(configStr, "# BuildEx entry for "+host1) {
		t.Errorf("Host1 entry should be removed from SSH config. Config:\n%s", configStr)
	}

	// Second host should remain
	if !strings.Contains(configStr, "# BuildEx entry for "+host2) {
		t.Errorf("Host2 entry should remain in SSH config. Config:\n%s", configStr)
	}
}

func TestHostKeyExists(t *testing.T) {
	// Create temporary directory for testing
	tempDir := t.TempDir()
	knownHostsPath := filepath.Join(tempDir, "known_hosts")

	// Create known_hosts file with sample entry
	content := `192.168.1.100 ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQC...
|1|abcdef==|ghijkl== ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQD...
192.168.1.101 ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFoo...`

	err := os.WriteFile(knownHostsPath, []byte(content), 0600)
	if err != nil {
		t.Fatalf("Failed to create known_hosts file: %v", err)
	}

	cfg := &config.Config{SSH: config.SSHConfig{}}
	helper := NewHelper(cfg)

	// Test existing host
	if !helper.hostKeyExists(knownHostsPath, "192.168.1.100") {
		t.Error("Should find existing host key")
	}

	// Test non-existing host
	if helper.hostKeyExists(knownHostsPath, "192.168.1.200") {
		t.Error("Should not find non-existing host key")
	}

	// Test with non-existing file
	if helper.hostKeyExists("/nonexistent/path", "192.168.1.100") {
		t.Error("Should return false for non-existing file")
	}
}

func TestAddHostKey(t *testing.T) {
	// This test requires ssh-keyscan to be available
	// Skip if not available
	if _, err := os.Stat("/usr/bin/ssh-keyscan"); os.IsNotExist(err) {
		if _, err := os.Stat("/bin/ssh-keyscan"); os.IsNotExist(err) {
			t.Skip("ssh-keyscan not available")
		}
	}

	// Create temporary directory for testing
	tempDir := t.TempDir()

	// Mock home directory
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempDir)
	defer os.Setenv("HOME", originalHome)

	cfg := &config.Config{
		SSH: config.SSHConfig{
			StrictHost: true,
		},
	}

	helper := NewHelper(cfg)

	// Test with a known public host (github.com)
	ctx := context.Background()
	err := helper.AddHostKey(ctx, "github.com")
	if err != nil {
		t.Logf("AddHostKey failed (this may be expected in CI): %v", err)
		return // Don't fail the test as network may not be available
	}

	// Check that known_hosts file was created and contains the host
	knownHostsPath := filepath.Join(tempDir, ".ssh", "known_hosts")
	content, err := os.ReadFile(knownHostsPath)
	if err != nil {
		t.Fatalf("Failed to read known_hosts: %v", err)
	}

	// ssh-keyscan -H produces hashed entries, so we check for any content
	if len(content) == 0 {
		t.Error("known_hosts file should not be empty after adding host key")
	}
}

func TestTestConnection(t *testing.T) {
	cfg := &config.Config{
		SSH: config.SSHConfig{
			User:       "testuser",
			Timeout:    5 * time.Second,
			StrictHost: false,
		},
	}

	helper := NewHelper(cfg)
	ctx := context.Background()

	// Test with invalid host (should fail)
	err := helper.TestConnection(ctx, "192.0.2.1") // RFC 5737 test address
	if err == nil {
		t.Error("Expected connection to fail for invalid host")
	}
}

func TestSSHConfigStrictHost(t *testing.T) {
	tests := []struct {
		name       string
		strictHost bool
		expectOpts []string
		notExpect  []string
	}{
		{
			name:       "strict host enabled",
			strictHost: true,
			expectOpts: []string{},
			notExpect: []string{
				"StrictHostKeyChecking no",
				"UserKnownHostsFile /dev/null",
			},
		},
		{
			name:       "strict host disabled",
			strictHost: false,
			expectOpts: []string{
				"StrictHostKeyChecking no",
				"UserKnownHostsFile /dev/null",
			},
			notExpect: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create temporary directory for testing
			tempDir := t.TempDir()

			// Mock home directory
			originalHome := os.Getenv("HOME")
			os.Setenv("HOME", tempDir)
			defer os.Setenv("HOME", originalHome)

			cfg := &config.Config{
				SSH: config.SSHConfig{
					User:       "testuser",
					StrictHost: tt.strictHost,
				},
			}

			helper := NewHelper(cfg)
			host := "192.168.1.100"

			err := helper.CreateSSHConfig(host)
			if err != nil {
				t.Fatalf("CreateSSHConfig failed: %v", err)
			}

			configPath := filepath.Join(tempDir, ".ssh", "config")
			content, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatalf("Failed to read SSH config: %v", err)
			}

			configStr := string(content)

			// Check expected options are present
			for _, expected := range tt.expectOpts {
				if !strings.Contains(configStr, expected) {
					t.Errorf("SSH config should contain %q", expected)
				}
			}

			// Check unwanted options are not present
			for _, notExpected := range tt.notExpect {
				if strings.Contains(configStr, notExpected) {
					t.Errorf("SSH config should not contain %q", notExpected)
				}
			}
		})
	}
}

func TestCreateSSHConfigWithKey(t *testing.T) {
	// Create temporary directory for testing
	tempDir := t.TempDir()

	// Mock home directory
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempDir)
	defer os.Setenv("HOME", originalHome)

	cfg := &config.Config{
		SSH: config.SSHConfig{
			User:       "testuser",
			Port:       22,
			StrictHost: false,
		},
	}

	helper := NewHelper(cfg)
	host := "192.168.1.100"
	// Create a mock key file in temp directory
	customKeyPath := filepath.Join(tempDir, "custom-key")
	err := os.WriteFile(customKeyPath, []byte("mock key content"), 0600)
	if err != nil {
		t.Fatalf("Failed to create mock key file: %v", err)
	}

	err = helper.CreateSSHConfigWithKey(host, customKeyPath)
	if err != nil {
		t.Fatalf("CreateSSHConfigWithKey failed: %v", err)
	}

	// Check that config file was created
	configPath := filepath.Join(tempDir, ".ssh", "config")
	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("Failed to read SSH config: %v", err)
	}

	configStr := string(content)

	// Check that config contains the custom key path
	expectedEntries := []string{
		"# BuildEx entry for " + host,
		"Host " + host,
		"HostName " + host,
		"User testuser",
		"IdentityFile " + customKeyPath,
		"IdentitiesOnly yes",
		"StrictHostKeyChecking no",
		"UserKnownHostsFile /dev/null",
	}

	for _, expected := range expectedEntries {
		if !strings.Contains(configStr, expected) {
			t.Errorf("SSH config missing expected entry: %q\nConfig content:\n%s", expected, configStr)
		}
	}
}
