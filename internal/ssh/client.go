package ssh

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/x-qdo/buildex/internal/config"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// Client wraps SSH client functionality for EC2 instances
type Client struct {
	config     *config.Config
	sshConfig  *ssh.ClientConfig
	connection *ssh.Client
	host       string
	port       int
}

// NewClient creates a new SSH client
func NewClient(cfg *config.Config, host string, keyPath string) (*Client, error) {
	// Read private key
	key, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read private key: %w", err)
	}

	// Parse private key
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("failed to parse private key: %w", err)
	}

	// Create SSH config
	sshConfig := &ssh.ClientConfig{
		User: cfg.SSH.User,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		Timeout: cfg.SSH.Timeout,
	}

	// Handle host key verification
	if cfg.SSH.StrictHost {
		hostKeyCallback, err := createHostKeyCallback()
		if err != nil {
			return nil, fmt.Errorf("failed to create host key callback: %w", err)
		}
		sshConfig.HostKeyCallback = hostKeyCallback
	} else {
		sshConfig.HostKeyCallback = ssh.InsecureIgnoreHostKey()
	}

	return &Client{
		config:    cfg,
		sshConfig: sshConfig,
		host:      host,
		port:      cfg.SSH.Port,
	}, nil
}

// Connect establishes SSH connection
func (c *Client) Connect(ctx context.Context) error {
	address := fmt.Sprintf("%s:%d", c.host, c.port)

	logrus.WithField("address", address).Debug("Connecting to SSH")

	// Create a dialer with timeout
	dialer := &net.Dialer{
		Timeout: c.config.SSH.Timeout,
	}

	// Dial with context
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return fmt.Errorf("failed to dial: %w", err)
	}

	// Create SSH connection
	sshConn, chans, reqs, err := ssh.NewClientConn(conn, address, c.sshConfig)
	if err != nil {
		conn.Close()
		return fmt.Errorf("failed to create SSH connection: %w", err)
	}

	c.connection = ssh.NewClient(sshConn, chans, reqs)

	logrus.WithField("address", address).Info("SSH connection established")
	return nil
}

// Close closes the SSH connection
func (c *Client) Close() error {
	if c.connection != nil {
		return c.connection.Close()
	}
	return nil
}

// ExecuteCommand executes a command on the remote host
func (c *Client) ExecuteCommand(ctx context.Context, command string) (string, error) {
	if c.connection == nil {
		return "", fmt.Errorf("not connected")
	}

	session, err := c.connection.NewSession()
	if err != nil {
		return "", fmt.Errorf("failed to create session: %w", err)
	}
	defer session.Close()

	logrus.WithField("command", command).Debug("Executing SSH command")

	// Set up context cancellation
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			session.Signal(ssh.SIGTERM)
			time.Sleep(2 * time.Second)
			session.Signal(ssh.SIGKILL)
		case <-done:
		}
	}()

	output, err := session.CombinedOutput(command)
	close(done)

	if err != nil {
		return string(output), fmt.Errorf("command failed: %w", err)
	}

	return string(output), nil
}

// ExecuteCommandWithOutput executes a command and streams output
func (c *Client) ExecuteCommandWithOutput(ctx context.Context, command string, stdout, stderr io.Writer) error {
	if c.connection == nil {
		return fmt.Errorf("not connected")
	}

	session, err := c.connection.NewSession()
	if err != nil {
		return fmt.Errorf("failed to create session: %w", err)
	}
	defer session.Close()

	logrus.WithField("command", command).Debug("Executing SSH command with output")

	// Set up output pipes
	session.Stdout = stdout
	session.Stderr = stderr

	// Set up context cancellation
	done := make(chan error, 1)
	go func() {
		done <- session.Run(command)
	}()

	select {
	case <-ctx.Done():
		session.Signal(ssh.SIGTERM)
		time.Sleep(2 * time.Second)
		session.Signal(ssh.SIGKILL)
		return ctx.Err()
	case err := <-done:
		return err
	}
}

// WaitForReady waits for the SSH connection to be ready and responsive
func (c *Client) WaitForReady(ctx context.Context) error {
	logrus.WithField("host", c.host).Info("Waiting for SSH to be ready")

	timeout := time.After(c.config.Timeouts.SSHConnect)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-timeout:
			return fmt.Errorf("timeout waiting for SSH to be ready")
		case <-ticker.C:
			if err := c.Connect(ctx); err != nil {
				logrus.WithError(err).Debug("SSH connection failed, retrying")
				continue
			}

			// Test with a simple command
			_, err := c.ExecuteCommand(ctx, "echo 'ready'")
			if err != nil {
				logrus.WithError(err).Debug("SSH command failed, retrying")
				c.Close()
				continue
			}

			logrus.WithField("host", c.host).Info("SSH connection is ready")
			return nil
		}
	}
}

// WaitForDockerReady waits for Docker to be ready on the remote host
func (c *Client) WaitForDockerReady(ctx context.Context) error {
	logrus.WithField("host", c.host).Info("Waiting for Docker to be ready")

	timeout := time.After(c.config.Timeouts.DockerReady)
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-timeout:
			return fmt.Errorf("timeout waiting for Docker to be ready")
		case <-ticker.C:
			// Check if buildex-ready marker exists (from user data)
			_, err := c.ExecuteCommand(ctx, "test -f /tmp/buildex-ready")
			if err != nil {
				logrus.Debug("Buildex setup not complete, waiting")
				continue
			}

			// Test Docker daemon
			_, err = c.ExecuteCommand(ctx, "docker info")
			if err != nil {
				logrus.WithError(err).Debug("Docker not ready, waiting")
				continue
			}

			// Test that docker can run containers
			_, err = c.ExecuteCommand(ctx, "docker run --rm hello-world")
			if err != nil {
				logrus.WithError(err).Debug("Docker container test failed, waiting")
				continue
			}

			logrus.WithField("host", c.host).Info("Docker is ready")
			return nil
		}
	}
}

// SetupDocker ensures Docker is properly configured for buildx
func (c *Client) SetupDocker(ctx context.Context) error {
	logrus.WithField("host", c.host).Info("Setting up Docker for buildx")

	commands := []string{
		"docker info",
	}

	for _, cmd := range commands {
		logrus.WithField("command", cmd).Debug("Executing setup command")
		_, err := c.ExecuteCommand(ctx, cmd)
		if err != nil {
			// Some commands may fail (like usermod if user already in group), log but continue
			logrus.WithError(err).WithField("command", cmd).Warn("Setup command failed")
		}
	}

	// Final verification
	output, err := c.ExecuteCommand(ctx, "docker version --format '{{.Server.Version}}'")
	if err != nil {
		return fmt.Errorf("Docker setup verification failed: %w", err)
	}

	logrus.WithFields(logrus.Fields{
		"host":           c.host,
		"docker_version": strings.TrimSpace(output),
	}).Info("Docker setup completed")

	return nil
}

// GetDockerContext returns the Docker context string for buildx
func (c *Client) GetDockerContext() string {
	return fmt.Sprintf("ssh://%s@%s", c.config.SSH.User, c.host)
}

// CopyFile copies a file to the remote host
func (c *Client) CopyFile(ctx context.Context, localPath, remotePath string) error {
	if c.connection == nil {
		return fmt.Errorf("not connected")
	}

	// Read local file
	data, err := os.ReadFile(localPath)
	if err != nil {
		return fmt.Errorf("failed to read local file: %w", err)
	}

	// Get file info for permissions
	info, err := os.Stat(localPath)
	if err != nil {
		return fmt.Errorf("failed to stat local file: %w", err)
	}

	// Create SCP session
	session, err := c.connection.NewSession()
	if err != nil {
		return fmt.Errorf("failed to create session: %w", err)
	}
	defer session.Close()

	// Set up SCP command
	go func() {
		w, _ := session.StdinPipe()
		defer w.Close()

		fmt.Fprintf(w, "C%#o %d %s\n", info.Mode().Perm(), len(data), filepath.Base(remotePath))
		w.Write(data)
		fmt.Fprint(w, "\x00")
	}()

	cmd := fmt.Sprintf("scp -t %s", remotePath)
	return session.Run(cmd)
}

// createHostKeyCallback creates a host key callback for known_hosts
func createHostKeyCallback() (ssh.HostKeyCallback, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get home directory: %w", err)
	}

	knownHostsPath := filepath.Join(homeDir, ".ssh", "known_hosts")

	// Create .ssh directory if it doesn't exist
	sshDir := filepath.Dir(knownHostsPath)
	if err := os.MkdirAll(sshDir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create .ssh directory: %w", err)
	}

	// Create known_hosts file if it doesn't exist
	if _, err := os.Stat(knownHostsPath); os.IsNotExist(err) {
		if _, err := os.Create(knownHostsPath); err != nil {
			return nil, fmt.Errorf("failed to create known_hosts file: %w", err)
		}
	}

	return knownhosts.New(knownHostsPath)
}

// TestConnection tests the SSH connection without establishing a persistent connection
func TestConnection(cfg *config.Config, host, keyPath string) error {
	client, err := NewClient(cfg, host, keyPath)
	if err != nil {
		return fmt.Errorf("failed to create SSH client: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.SSH.Timeout)
	defer cancel()

	if err := client.Connect(ctx); err != nil {
		return fmt.Errorf("failed to connect: %w", err)
	}
	defer client.Close()

	_, err = client.ExecuteCommand(ctx, "echo 'test'")
	if err != nil {
		return fmt.Errorf("failed to execute test command: %w", err)
	}

	return nil
}
