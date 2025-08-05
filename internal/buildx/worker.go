package buildx

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/x-qdo/buildex/internal/config"
	"github.com/x-qdo/buildex/internal/ssh"
)

// Manager manages Docker buildx operations
type Manager struct {
	config    *config.Config
	sshHelper *ssh.Helper
	builderMu sync.Mutex // Protects builder creation
}

// NewManager creates a new buildx manager
func NewManager(cfg *config.Config) (*Manager, error) {
	return &Manager{
		config:    cfg,
		sshHelper: ssh.NewHelper(cfg),
	}, nil
}

// AddWorker adds a worker to the buildx builder
func (m *Manager) AddWorker(ctx context.Context, worker *config.WorkerState) error {
	logrus.WithField("worker", worker.Name).Info("Adding worker to buildx")

	builderName := worker.BuilderName
	if builderName == "" {
		builderName = m.config.Buildx.BuilderName
	}

	// Ensure builder exists - this must be done before adding any nodes
	if err := m.ensureBuilder(ctx, builderName); err != nil {
		return fmt.Errorf("failed to ensure builder: %w", err)
	}

	// Create SSH config for the worker with its specific SSH key
	if err := m.sshHelper.CreateSSHConfigWithKey(worker.PublicIP, worker.SSHKeyPath); err != nil {
		logrus.WithError(err).WithField("host", worker.PublicIP).Warn("Failed to create SSH config")
	}

	// Add host key if strict host checking is enabled
	if m.config.SSH.StrictHost {
		if err := m.sshHelper.AddHostKey(ctx, worker.PublicIP); err != nil {
			logrus.WithError(err).WithField("host", worker.PublicIP).Warn("Failed to add host key")
		}
	}

	// Create docker context for the worker
	contextName := fmt.Sprintf("buildex-%s", worker.Name)
	dockerEndpoint := fmt.Sprintf("ssh://%s@%s", m.config.SSH.User, worker.PublicIP)

	logrus.WithFields(logrus.Fields{
		"worker":   worker.Name,
		"context":  contextName,
		"endpoint": dockerEndpoint,
	}).Debug("Creating docker context for worker")

	// Create docker context
	createContextArgs := []string{"docker", "context", "create", contextName, "--docker", fmt.Sprintf("host=%s", dockerEndpoint)}
	logrus.WithFields(logrus.Fields{
		"worker":  worker.Name,
		"command": strings.Join(createContextArgs, " "),
	}).Debug("Executing docker context create command")

	cmd := exec.CommandContext(ctx, createContextArgs[0], createContextArgs[1:]...)

	if output, err := cmd.CombinedOutput(); err != nil {
		logrus.WithFields(logrus.Fields{
			"worker":  worker.Name,
			"command": strings.Join(createContextArgs, " "),
			"output":  string(output),
			"error":   err.Error(),
		}).Debug("Docker context creation failed, attempting update")

		// Context might already exist, try to update it
		updateContextArgs := []string{"docker", "context", "update", contextName, "--docker", fmt.Sprintf("host=%s", dockerEndpoint)}
		logrus.WithFields(logrus.Fields{
			"worker":  worker.Name,
			"command": strings.Join(updateContextArgs, " "),
		}).Debug("Executing docker context update command")

		cmd = exec.CommandContext(ctx, updateContextArgs[0], updateContextArgs[1:]...)

		if output, err := cmd.CombinedOutput(); err != nil {
			logrus.WithFields(logrus.Fields{
				"worker":  worker.Name,
				"command": strings.Join(updateContextArgs, " "),
				"output":  string(output),
				"error":   err.Error(),
			}).Debug("Docker context update failed")
			return fmt.Errorf("failed to create/update docker context: %s", string(output))
		}

		logrus.WithFields(logrus.Fields{
			"worker":  worker.Name,
			"command": strings.Join(updateContextArgs, " "),
			"output":  string(output),
		}).Debug("Docker context updated successfully")
	} else {
		logrus.WithFields(logrus.Fields{
			"worker":  worker.Name,
			"command": strings.Join(createContextArgs, " "),
			"output":  string(output),
		}).Debug("Docker context created successfully")
	}

	// Check if builder exists and get its current nodes to avoid duplicates
	existingNodes, err := m.getBuilderNodes(ctx, builderName)
	if err != nil {
		logrus.WithError(err).Warn("Failed to get existing builder nodes, proceeding anyway")
	} else {
		// Check if this worker is already in the builder
		for _, node := range existingNodes {
			if node.Endpoint == contextName {
				logrus.WithField("worker", worker.Name).Info("Worker already exists in builder, skipping addition")
				return nil
			}
		}
	}

	// Add node to existing builder using buildx create with --append
	// This will add the node to the existing builder, not create a new one
	addWorkerArgs := []string{"docker", "buildx", "create", "--append", "--name", builderName, "--platform", worker.Platform, contextName}

	logrus.WithFields(logrus.Fields{
		"worker":   worker.Name,
		"builder":  builderName,
		"platform": worker.Platform,
		"context":  contextName,
		"command":  strings.Join(addWorkerArgs, " "),
	}).Debug("Adding worker to buildx builder")

	cmd = exec.CommandContext(ctx, addWorkerArgs[0], addWorkerArgs[1:]...)

	if output, err := cmd.CombinedOutput(); err != nil {
		logrus.WithFields(logrus.Fields{
			"worker":  worker.Name,
			"command": strings.Join(addWorkerArgs, " "),
			"output":  string(output),
			"error":   err.Error(),
		}).Debug("Failed to add worker to buildx")
		return fmt.Errorf("failed to add worker to buildx: %s", string(output))
	} else {
		logrus.WithFields(logrus.Fields{
			"worker":  worker.Name,
			"command": strings.Join(addWorkerArgs, " "),
			"output":  string(output),
		}).Debug("Worker added to buildx successfully")
	}

	logrus.WithFields(logrus.Fields{
		"worker":   worker.Name,
		"builder":  builderName,
		"platform": worker.Platform,
		"endpoint": dockerEndpoint,
	}).Info("Worker added to buildx successfully")

	return nil
}

// RemoveWorker removes a worker from the buildx builder
func (m *Manager) RemoveWorker(ctx context.Context, worker *config.WorkerState) error {
	logrus.WithFields(logrus.Fields{
		"worker":   worker.Name,
		"platform": worker.Platform,
		"ip":       worker.PublicIP,
	}).Info("Removing worker from buildx")

	builderName := worker.BuilderName
	if builderName == "" {
		builderName = m.config.Buildx.BuilderName
	}

	// The node name matches the context name we created
	contextName := fmt.Sprintf("buildex-%s", worker.Name)
	nodeName := contextName

	// Remove node from builder using --leave flag
	logrus.WithFields(logrus.Fields{
		"builder": builderName,
		"node":    nodeName,
	}).Debug("Removing node from builder")

	cmd := exec.CommandContext(ctx, "docker", "buildx", "create",
		"--leave",
		"--name", builderName,
		"--node", nodeName)

	if output, err := cmd.CombinedOutput(); err != nil {
		logrus.WithError(err).WithFields(logrus.Fields{
			"output":  string(output),
			"builder": builderName,
			"node":    nodeName,
		}).Warn("Failed to remove node from builder")
		// Continue with cleanup anyway
	} else {
		logrus.WithFields(logrus.Fields{
			"builder": builderName,
			"node":    nodeName,
		}).Info("Node removed from builder successfully")
	}

	// Remove docker context
	logrus.WithField("context", contextName).Debug("Removing Docker context")
	cmd = exec.CommandContext(ctx, "docker", "context", "rm", "-f", contextName)
	if output, err := cmd.CombinedOutput(); err != nil {
		logrus.WithError(err).WithFields(logrus.Fields{
			"output":  string(output),
			"context": contextName,
		}).Warn("Failed to remove docker context")
	} else {
		logrus.WithField("context", contextName).Info("Docker context removed successfully")
	}

	// Remove SSH config and host key
	logrus.WithField("host", worker.PublicIP).Debug("Removing SSH configuration")
	if err := m.sshHelper.RemoveSSHConfig(worker.PublicIP); err != nil {
		logrus.WithError(err).WithField("host", worker.PublicIP).Warn("Failed to remove SSH config")
	} else {
		logrus.WithField("host", worker.PublicIP).Debug("SSH config removed successfully")
	}

	if err := m.sshHelper.RemoveHostKey(worker.PublicIP); err != nil {
		logrus.WithError(err).WithField("host", worker.PublicIP).Warn("Failed to remove host key")
	} else {
		logrus.WithField("host", worker.PublicIP).Debug("SSH host key removed successfully")
	}

	logrus.WithField("worker", worker.Name).Info("Worker removed from buildx")
	return nil
}

// ensureBuilder ensures the buildx builder exists
func (m *Manager) ensureBuilder(ctx context.Context, builderName string) error {
	// Use mutex to prevent race conditions when creating builder
	m.builderMu.Lock()
	defer m.builderMu.Unlock()

	// Check if builder exists
	inspectArgs := []string{"docker", "buildx", "inspect", builderName}
	logrus.WithFields(logrus.Fields{
		"builder": builderName,
		"command": strings.Join(inspectArgs, " "),
	}).Debug("Checking if builder exists")

	cmd := exec.CommandContext(ctx, inspectArgs[0], inspectArgs[1:]...)
	if err := cmd.Run(); err == nil {
		logrus.WithField("builder", builderName).Debug("Builder already exists")
		return nil // Builder exists
	}

	logrus.WithFields(logrus.Fields{
		"builder": builderName,
		"command": strings.Join(inspectArgs, " "),
	}).Debug("Builder does not exist, proceeding with creation")

	// Create builder without any initial context/node
	logrus.WithField("builder", builderName).Info("Creating buildx builder")

	args := []string{"docker", "buildx", "create", "--name", builderName}

	// Add driver options
	for _, opt := range m.config.Buildx.DriverOpts {
		args = append(args, "--driver-opt", opt)
	}

	// Debug log the exact command being executed
	logrus.WithFields(logrus.Fields{
		"builder": builderName,
		"command": strings.Join(args, " "),
		"args":    args,
	}).Debug("Executing buildx create command")

	cmd = exec.CommandContext(ctx, args[0], args[1:]...)
	if output, err := cmd.CombinedOutput(); err != nil {
		logrus.WithFields(logrus.Fields{
			"builder": builderName,
			"command": strings.Join(args, " "),
			"output":  string(output),
			"error":   err.Error(),
		}).Debug("Builder creation failed, checking for race condition")

		// If builder creation fails, it might be due to race condition
		// Check again if it exists now
		checkCmd := exec.CommandContext(ctx, "docker", "buildx", "inspect", builderName)
		if checkCmd.Run() == nil {
			logrus.WithField("builder", builderName).Info("Builder exists after race condition")
			return nil
		}
		return fmt.Errorf("failed to create builder: %s", string(output))
	} else {
		logrus.WithFields(logrus.Fields{
			"builder": builderName,
			"command": strings.Join(args, " "),
			"output":  string(output),
		}).Debug("Buildx builder created successfully")
	}

	logrus.WithField("builder", builderName).Info("Buildx builder created successfully")
	return nil
}

// getBuilderNodes returns the nodes in a builder
func (m *Manager) getBuilderNodes(ctx context.Context, builderName string) ([]BuilderNode, error) {
	cmd := exec.CommandContext(ctx, "docker", "buildx", "inspect", builderName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("failed to inspect builder: %s", string(output))
	}

	// Parse the output to extract node information
	lines := strings.Split(string(output), "\n")
	var nodes []BuilderNode
	var currentNode *BuilderNode
	var inNodesSection bool

	for _, line := range lines {
		originalLine := line
		line = strings.TrimSpace(line)

		// Check if we're entering the Nodes section
		if line == "Nodes:" {
			inNodesSection = true
			continue
		}

		// If we're in the nodes section, look for node information
		if inNodesSection && strings.HasPrefix(originalLine, "Name:") {
			// Save previous node if exists
			if currentNode != nil {
				nodes = append(nodes, *currentNode)
			}

			// Start new node
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				currentNode = &BuilderNode{
					Name: strings.TrimSpace(parts[1]),
				}
			}
		} else if currentNode != nil && inNodesSection {
			// Parse node properties (these lines are indented under the node)
			if strings.HasPrefix(line, "Endpoint:") {
				parts := strings.SplitN(line, ":", 2)
				if len(parts) == 2 {
					currentNode.Endpoint = strings.TrimSpace(parts[1])
				}
			} else if strings.HasPrefix(line, "Status:") {
				parts := strings.SplitN(line, ":", 2)
				if len(parts) == 2 {
					currentNode.Status = strings.TrimSpace(parts[1])
				}
			} else if strings.HasPrefix(line, "Platforms:") {
				parts := strings.SplitN(line, ":", 2)
				if len(parts) == 2 {
					currentNode.Platform = strings.TrimSpace(parts[1])
				}
			}
		}
	}

	// Add the last node
	if currentNode != nil {
		nodes = append(nodes, *currentNode)
	}

	return nodes, nil
}

// BuilderNode represents a node in a buildx builder
type BuilderNode struct {
	Name     string
	Endpoint string
	Status   string
	Platform string
}

// ListBuilders lists all buildx builders
func (m *Manager) ListBuilders(ctx context.Context) ([]string, error) {
	cmd := exec.CommandContext(ctx, "docker", "buildx", "ls")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("failed to list builders: %s", string(output))
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	var builders []string
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if i == 0 || line == "" {
			continue // Skip header line and empty lines
		}

		// Extract builder name (first column, before any spaces)
		parts := strings.Fields(line)
		if len(parts) > 0 && !strings.HasPrefix(parts[0], " ") {
			// This is a builder line (not indented)
			builders = append(builders, parts[0])
		}
	}

	return builders, nil
}

// CleanupBuilder removes a specific builder
func (m *Manager) CleanupBuilder(ctx context.Context, builderName string) error {
	logrus.WithField("builder", builderName).Info("Removing builder")

	cmd := exec.CommandContext(ctx, "docker", "buildx", "rm", builderName)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to remove builder: %s", string(output))
	}

	return nil
}

// CleanupEmptyBuilders removes builders that have no nodes
func (m *Manager) CleanupEmptyBuilders(ctx context.Context) error {
	builders, err := m.ListBuilders(ctx)
	if err != nil {
		return fmt.Errorf("failed to list builders: %w", err)
	}

	for _, builder := range builders {
		if !strings.HasPrefix(builder, "buildex-") {
			continue // Only cleanup buildex builders
		}

		nodes, err := m.getBuilderNodes(ctx, builder)
		if err != nil {
			logrus.WithError(err).WithField("builder", builder).Warn("Failed to get builder nodes")
			continue
		}

		if len(nodes) == 0 {
			logrus.WithField("builder", builder).Info("Removing empty builder")

			cmd := exec.CommandContext(ctx, "docker", "buildx", "rm", builder)
			if output, err := cmd.CombinedOutput(); err != nil {
				logrus.WithError(err).WithField("output", string(output)).Warn("Failed to remove empty builder")
			}
		}
	}

	return nil
}

// UseBuilder sets the active buildx builder
func (m *Manager) UseBuilder(ctx context.Context, builderName string) error {
	cmd := exec.CommandContext(ctx, "docker", "buildx", "use", builderName)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to use builder: %s", string(output))
	}

	return nil
}

// BootstrapBuilder initializes the buildx builder
func (m *Manager) BootstrapBuilder(ctx context.Context, builderName string) error {
	logrus.WithField("builder", builderName).Info("Bootstrapping buildx builder")

	cmd := exec.CommandContext(ctx, "docker", "buildx", "inspect", "--bootstrap", builderName)
	if output, err := cmd.CombinedOutput(); err != nil {
		logrus.WithFields(logrus.Fields{
			"builder": builderName,
			"output":  string(output),
			"error":   err.Error(),
		}).Debug("Failed to bootstrap builder")
		return fmt.Errorf("failed to bootstrap builder: %s", string(output))
	}

	logrus.WithField("builder", builderName).Info("Builder bootstrapped successfully")
	return nil
}

func (m *Manager) InspectBuilder(ctx context.Context, builderName string) (*BuilderInfo, error) {
	cmd := exec.CommandContext(ctx, "docker", "buildx", "inspect", builderName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("failed to inspect builder: %s", string(output))
	}

	// Parse builder information
	info := &BuilderInfo{
		Name:  builderName,
		Nodes: []BuilderNode{},
	}

	lines := strings.Split(string(output), "\n")
	var currentNode *BuilderNode

	for _, line := range lines {
		line = strings.TrimSpace(line)

		if strings.HasPrefix(line, "Name:") {
			parts := strings.Fields(line)
			if len(parts) >= 2 && currentNode == nil {
				// This is the builder name line
				continue
			}
			// This is a node name
			if currentNode != nil {
				info.Nodes = append(info.Nodes, *currentNode)
			}
			currentNode = &BuilderNode{
				Name: strings.TrimSpace(strings.TrimPrefix(line, "Name:")),
			}
		} else if strings.HasPrefix(line, "Endpoint:") && currentNode != nil {
			currentNode.Endpoint = strings.TrimSpace(strings.TrimPrefix(line, "Endpoint:"))
		} else if strings.HasPrefix(line, "Status:") && currentNode != nil {
			currentNode.Status = strings.TrimSpace(strings.TrimPrefix(line, "Status:"))
		} else if strings.HasPrefix(line, "Platforms:") && currentNode != nil {
			currentNode.Platform = strings.TrimSpace(strings.TrimPrefix(line, "Platforms:"))
		}
	}

	// Add the last node
	if currentNode != nil {
		info.Nodes = append(info.Nodes, *currentNode)
	}

	return info, nil
}

// BuilderInfo contains information about a buildx builder
type BuilderInfo struct {
	Name      string
	Driver    string
	Nodes     []BuilderNode
	CreatedAt time.Time
}

// IsWorkerHealthy checks if a worker is healthy and responsive
func (m *Manager) IsWorkerHealthy(ctx context.Context, worker *config.WorkerState) bool {
	// Create docker endpoint for testing
	dockerEndpoint := fmt.Sprintf("ssh://%s@%s", m.config.SSH.User, worker.PublicIP)

	// Test SSH connection first
	if err := m.sshHelper.TestConnection(ctx, worker.PublicIP); err != nil {
		logrus.WithError(err).WithField("host", worker.PublicIP).Debug("SSH connection test failed")
		return false
	}

	// Test docker connection
	cmd := exec.CommandContext(ctx, "docker", "-H", dockerEndpoint, "version")

	if err := cmd.Run(); err != nil {
		return false
	}

	return true
}

// GetWorkerPlatforms returns the platforms supported by a worker
func (m *Manager) GetWorkerPlatforms(ctx context.Context, worker *config.WorkerState) ([]string, error) {
	dockerEndpoint := fmt.Sprintf("ssh://%s@%s", m.config.SSH.User, worker.PublicIP)

	cmd := exec.CommandContext(ctx, "docker", "-H", dockerEndpoint, "version", "--format", "{{.Server.Os}}/{{.Server.Arch}}")

	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("failed to get worker platforms: %s", string(output))
	}

	platform := strings.TrimSpace(string(output))
	return []string{platform}, nil
}
