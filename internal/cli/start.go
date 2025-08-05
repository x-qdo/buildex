package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/x-qdo/buildex/internal/aws"
	"github.com/x-qdo/buildex/internal/buildx"
	"github.com/x-qdo/buildex/internal/config"
	"github.com/x-qdo/buildex/internal/ssh"
)

var startCmd = &cobra.Command{
	Use:   "start [worker-name]",
	Short: "Start buildx workers on EC2 instances",
	Long: `Start one or more buildx workers on EC2 instances. Workers can be started
for specific platforms or all configured platforms.

Examples:
  # Start workers for all platforms
  buildex start

  # Start a specific named worker
  buildex start my-worker

  # Start workers for specific platforms
  buildex start --platform linux/amd64,linux/arm64

  # Start spot instances
  buildex start --spot

  # Start with custom instance type
  buildex start --instance-type m5.large`,
	RunE: runStart,
}

var (
	startPlatforms    []string
	startSpot         bool
	startInstanceType string
	startWorkerName   string
	startSaveState    bool
	startWait         bool
)

func init() {
	rootCmd.AddCommand(startCmd)

	startCmd.Flags().StringSliceVarP(&startPlatforms, "platform", "p", nil, "Target platforms (e.g., linux/amd64,linux/arm64)")
	startCmd.Flags().BoolVar(&startSpot, "spot", false, "Use spot instances")
	startCmd.Flags().StringVar(&startInstanceType, "instance-type", "", "EC2 instance type")
	startCmd.Flags().StringVar(&startWorkerName, "name", "", "Worker name prefix")
	startCmd.Flags().BoolVar(&startSaveState, "save-state", true, "Save worker state to file")
	startCmd.Flags().BoolVar(&startWait, "wait", true, "Wait for workers to be ready")

	// Bind flags to viper
	viper.BindPFlag("start.platforms", startCmd.Flags().Lookup("platform"))
	viper.BindPFlag("start.spot", startCmd.Flags().Lookup("spot"))
	viper.BindPFlag("start.instance_type", startCmd.Flags().Lookup("instance-type"))
	viper.BindPFlag("start.save_state", startCmd.Flags().Lookup("save-state"))
	viper.BindPFlag("start.wait", startCmd.Flags().Lookup("wait"))
}

func runStart(cmd *cobra.Command, args []string) error {
	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Validate configuration
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}

	// Override config with command-line flags
	if startInstanceType != "" {
		cfg.AWS.InstanceType = startInstanceType
	}
	if startSpot {
		cfg.AWS.UseSpot = true
	}

	// Determine worker name
	workerName := startWorkerName
	if len(args) > 0 {
		workerName = args[0]
	}
	if workerName == "" {
		workerName = "worker"
	}

	// Determine platforms
	platforms := startPlatforms
	if len(platforms) == 0 {
		platforms = cfg.Buildx.Platforms
	}

	// Apply platform-specific overrides for command-line flags
	if startInstanceType != "" {
		// Also set for all platforms if no platform-specific config exists
		if cfg.AWS.Platforms == nil {
			cfg.AWS.Platforms = make(map[string]*config.PlatformConfig)
		}
		for _, platform := range platforms {
			if _, exists := cfg.AWS.Platforms[platform]; !exists {
				cfg.AWS.Platforms[platform] = &config.PlatformConfig{
					InstanceType: startInstanceType,
				}
			} else if cfg.AWS.Platforms[platform].InstanceType == "" {
				cfg.AWS.Platforms[platform].InstanceType = startInstanceType
			}
		}
	}
	if startSpot {
		// Also set for all platforms if no platform-specific config exists
		if cfg.AWS.Platforms == nil {
			cfg.AWS.Platforms = make(map[string]*config.PlatformConfig)
		}
		for _, platform := range platforms {
			if _, exists := cfg.AWS.Platforms[platform]; !exists {
				cfg.AWS.Platforms[platform] = &config.PlatformConfig{
					UseSpot: &startSpot,
				}
			} else if cfg.AWS.Platforms[platform].UseSpot == nil {
				useSpotPtr := startSpot
				cfg.AWS.Platforms[platform].UseSpot = &useSpotPtr
			}
		}
	}

	logrus.WithFields(logrus.Fields{
		"worker_name": workerName,
		"platforms":   platforms,
		"spot":        cfg.AWS.UseSpot,
		"instance":    cfg.AWS.InstanceType,
	}).Info("Starting buildx workers")

	// Create AWS manager
	ec2Manager, err := aws.NewEC2Manager(cfg)
	if err != nil {
		return fmt.Errorf("failed to create EC2 manager: %w", err)
	}

	// Create buildx manager
	buildxManager, err := buildx.NewManager(cfg)
	if err != nil {
		return fmt.Errorf("failed to create buildx manager: %w", err)
	}

	logrus.WithFields(logrus.Fields{
		"platforms": platforms,
		"builder":   config.GetPermanentBuilderName(),
	}).Debug("Buildx manager created, preparing to launch workers")

	// Load state
	state, err := config.LoadState()
	if err != nil {
		return fmt.Errorf("failed to load state: %w", err)
	}

	logrus.Info("Starting worker launch process")

	// Launch workers for each platform
	workers := make([]*config.WorkerState, 0, len(platforms))
	ctx := context.Background()

	for _, platform := range platforms {
		platformWorkerName := config.GeneratePermanentWorkerName(workerName, platform)

		// Check if worker already exists
		if existingWorker, exists := state.GetWorker(platformWorkerName); exists {
			if existingWorker.Status == "running" {
				logrus.WithField("worker", platformWorkerName).Info("Worker already running")
				continue
			}
		}

		logrus.WithField("worker", platformWorkerName).Info("Launching worker")

		// Get platform-specific configuration
		useSpot := cfg.GetUseSpotForPlatform(platform)
		instanceType := cfg.GetInstanceTypeForPlatform(platform)

		logrus.WithFields(logrus.Fields{
			"worker":        platformWorkerName,
			"platform":      platform,
			"instance_type": instanceType,
			"use_spot":      useSpot,
		}).Info("Launching worker with platform-specific configuration")

		logrus.WithFields(logrus.Fields{
			"worker":   platformWorkerName,
			"platform": platform,
		}).Debug("Starting EC2 instance launch process")

		// Launch instance
		instanceInfo, err := ec2Manager.LaunchInstance(ctx, platformWorkerName, platform, useSpot)
		if err != nil {
			return fmt.Errorf("failed to launch instance for %s: %w", platform, err)
		}

		// Create worker state
		worker := &config.WorkerState{
			Name:         platformWorkerName,
			InstanceID:   instanceInfo.InstanceID,
			PublicIP:     instanceInfo.PublicIP,
			PrivateIP:    instanceInfo.PrivateIP,
			Platform:     platform,
			InstanceType: instanceInfo.InstanceType,
			Region:       cfg.AWS.Region,
			IsSpot:       instanceInfo.IsSpot,
			Status:       "starting",
			CreatedAt:    time.Now(),
			UpdatedAt:    time.Now(),
			Tags:         instanceInfo.Tags,
			BuilderName:  config.GetPermanentBuilderName(),
		}

		// Add to state
		state.AddWorker(worker)
		workers = append(workers, worker)

		if startSaveState {
			if err := config.SaveState(state); err != nil {
				logrus.WithError(err).Warn("Failed to save state")
			}
		}

		logrus.WithFields(logrus.Fields{
			"worker":      platformWorkerName,
			"instance_id": instanceInfo.InstanceID,
			"public_ip":   instanceInfo.PublicIP,
			"platform":    platform,
		}).Info("Worker instance launched")

		logrus.WithFields(logrus.Fields{
			"worker":       platformWorkerName,
			"instance_id":  instanceInfo.InstanceID,
			"builder_name": config.GetPermanentBuilderName(),
		}).Debug("Worker state created and saved")
	}

	if !startWait {
		logrus.Info("Worker launch process completed")
		fmt.Printf("Started %d workers. Use 'buildex status' to check their status.\n", len(workers))
		return nil
	}

	logrus.Info("Starting worker setup process")

	// Wait for workers to be ready and set up buildx
	for _, worker := range workers {

		logrus.WithFields(logrus.Fields{
			"worker":     worker.Name,
			"public_ip":  worker.PublicIP,
			"private_ip": worker.PrivateIP,
			"platform":   worker.Platform,
		}).Debug("Starting worker setup process")

		if err := setupWorker(ctx, cfg, worker); err != nil {
			worker.Status = "failed"
			logrus.WithError(err).WithField("worker", worker.Name).Error("Failed to setup worker")

			if startSaveState {
				state.AddWorker(worker)
				config.SaveState(state)
			}
			continue
		}

		worker.Status = "running"
		worker.UpdatedAt = time.Now()

		// Add worker to buildx

		logrus.WithFields(logrus.Fields{
			"worker":       worker.Name,
			"builder_name": worker.BuilderName,
			"platform":     worker.Platform,
		}).Debug("Adding worker to buildx builder")

		if err := buildxManager.AddWorker(ctx, worker); err != nil {
			logrus.WithError(err).WithField("worker", worker.Name).Error("Failed to add worker to buildx")
			worker.Status = "error"
		} else {
			logrus.WithFields(logrus.Fields{
				"worker":   worker.Name,
				"platform": worker.Platform,
			}).Debug("Worker successfully added to buildx")
		}

		if startSaveState {
			state.AddWorker(worker)
			if err := config.SaveState(state); err != nil {
				logrus.WithError(err).Warn("Failed to save state")
			}
		}

		logrus.WithFields(logrus.Fields{
			"worker":   worker.Name,
			"platform": worker.Platform,
			"status":   worker.Status,
		}).Info("Worker setup completed")
	}

	logrus.Info("Worker setup process completed")

	// Count successful workers
	successCount := 0
	for _, worker := range workers {
		if worker.Status == "running" {
			successCount++
		}
	}

	if successCount == 0 {
		return fmt.Errorf("failed to start any workers")
	}

	fmt.Printf("Successfully started %d/%d workers.\n", successCount, len(workers))

	if successCount < len(workers) {
		fmt.Printf("Some workers failed to start. Use 'buildex status' for details.\n")
	}

	// Bootstrap and use the builder if we have successful workers
	if successCount > 0 {
		builderName := config.GetPermanentBuilderName()

		logrus.WithField("builder", builderName).Info("Starting builder bootstrap process")
		logrus.WithField("builder", builderName).Info("Bootstrapping buildx builder")
		if err := buildxManager.BootstrapBuilder(ctx, builderName); err != nil {
			logrus.WithError(err).WithField("builder", builderName).Warn("Failed to bootstrap builder")
			fmt.Printf("Warning: Failed to bootstrap builder %s: %v\n", builderName, err)
		} else {
			logrus.WithField("builder", builderName).Info("Setting builder as active")
			if err := buildxManager.UseBuilder(ctx, builderName); err != nil {
				logrus.WithError(err).WithField("builder", builderName).Warn("Failed to set builder as active")
				fmt.Printf("Warning: Failed to set builder %s as active: %v\n", builderName, err)
			} else {
				logrus.WithField("builder", builderName).Info("Builder is now active and ready for builds")
			}
		}

		logrus.WithField("builder", builderName).Info("Builder bootstrap process completed")
	}

	// Display worker information
	fmt.Println("\nWorker Status:")
	for _, worker := range workers {
		status := "✓"
		if worker.Status != "running" {
			status = "✗"
		}
		fmt.Printf("  %s %s (%s) - %s - %s\n",
			status, worker.Name, worker.Platform, worker.PublicIP, worker.Status)
	}

	return nil
}

func setupWorker(ctx context.Context, cfg *config.Config, worker *config.WorkerState) error {
	logrus.WithFields(logrus.Fields{
		"worker":    worker.Name,
		"public_ip": worker.PublicIP,
	}).Debug("Starting worker setup")

	// Get SSH key path
	keyPath, err := aws.GetSSHKeyPath(cfg.AWS.KeyName)
	if err != nil {
		return fmt.Errorf("failed to get SSH key path: %w", err)
	}

	logrus.WithFields(logrus.Fields{
		"worker":   worker.Name,
		"key_path": keyPath,
	}).Debug("SSH key path resolved")

	// Create SSH client
	logrus.WithFields(logrus.Fields{
		"worker":    worker.Name,
		"public_ip": worker.PublicIP,
	}).Debug("Creating SSH client")

	sshClient, err := ssh.NewClient(cfg, worker.PublicIP, keyPath)
	if err != nil {
		return fmt.Errorf("failed to create SSH client: %w", err)
	}
	defer sshClient.Close()

	logrus.WithField("worker", worker.Name).Debug("SSH client created successfully")

	// Wait for SSH to be ready
	logrus.WithField("worker", worker.Name).Debug("Waiting for SSH to be ready")
	if err := sshClient.WaitForReady(ctx); err != nil {
		return fmt.Errorf("SSH not ready: %w", err)
	}
	logrus.WithField("worker", worker.Name).Debug("SSH is ready")

	// Wait for Docker to be ready
	logrus.WithField("worker", worker.Name).Debug("Waiting for Docker to be ready")
	if err := sshClient.WaitForDockerReady(ctx); err != nil {
		return fmt.Errorf("Docker not ready: %w", err)
	}
	logrus.WithField("worker", worker.Name).Debug("Docker is ready")

	// Setup Docker for buildx
	logrus.WithField("worker", worker.Name).Debug("Setting up Docker for buildx")
	if err := sshClient.SetupDocker(ctx); err != nil {
		return fmt.Errorf("failed to setup Docker: %w", err)
	}
	logrus.WithField("worker", worker.Name).Debug("Docker setup completed")

	// Store SSH key path in worker state
	worker.SSHKeyPath = keyPath

	logrus.WithFields(logrus.Fields{
		"worker":     worker.Name,
		"ssh_key":    keyPath,
		"setup_time": time.Since(worker.CreatedAt),
	}).Debug("Worker setup completed successfully")

	return nil
}
