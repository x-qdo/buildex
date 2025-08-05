package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/x-qdo/buildex/internal/aws"
	"github.com/x-qdo/buildex/internal/buildx"
	"github.com/x-qdo/buildex/internal/config"
)

var buildCmd = &cobra.Command{
	Use:   "build [context]",
	Short: "Build Docker images using on-demand EC2 buildx workers",
	Long: `Build Docker images using on-demand EC2 buildx workers. This command will:
1. Launch EC2 instances for the specified platforms
2. Configure them as Docker buildx workers
3. Run the build using the remote workers
4. Terminate the instances when done (unless --keep is specified)

Examples:
  # Basic build
  buildex build .

  # Multi-platform build
  buildex build . --platform linux/amd64,linux/arm64

  # Build with tags and push
  buildex build . --tag myapp:latest --push

  # Use spot instances
  buildex build . --spot --instance-type m5.large

  # Keep workers running after build
  buildex build . --keep

  # Automatically detect and use existing workers (default)
  buildex build . --auto-use-existing

  # Disable automatic existing worker detection
  buildex build . --auto-use-existing=false`,
	Args: cobra.MaximumNArgs(1),
	RunE: runBuild,
}

var (
	buildPlatforms       []string
	buildTags            []string
	buildFile            string
	buildTarget          string
	buildArgs            []string
	buildSecrets         []string
	buildPush            bool
	buildLoad            bool
	buildOutput          string
	buildSpot            bool
	buildInstanceType    string
	buildKeep            bool
	buildTimeout         time.Duration
	buildProgress        string
	buildAutoUseExisting bool
	buildProvenance      string
	buildContexts        []string
)

func init() {
	rootCmd.AddCommand(buildCmd)

	buildCmd.Flags().StringSliceVarP(&buildPlatforms, "platform", "p", nil, "Target platforms (e.g., linux/amd64,linux/arm64)")
	buildCmd.Flags().StringSliceVarP(&buildTags, "tag", "t", nil, "Name and optionally a tag in the 'name:tag' format")
	buildCmd.Flags().StringVarP(&buildFile, "file", "f", "", "Name of the Dockerfile")
	buildCmd.Flags().StringVar(&buildTarget, "target", "", "Set the target build stage to build")
	buildCmd.Flags().StringArrayVar(&buildArgs, "build-arg", nil, "Set build-time variables")
	buildCmd.Flags().StringArrayVar(&buildContexts, "build-context", nil, "Additional build contexts (e.g. mycontext=docker-image://myimage)")
	buildCmd.Flags().StringArrayVar(&buildSecrets, "secret", nil, "Secret file to expose to the build")
	buildCmd.Flags().BoolVar(&buildPush, "push", false, "Push the image to registry")
	buildCmd.Flags().BoolVar(&buildLoad, "load", false, "Load the single-platform build result to docker images")
	buildCmd.Flags().StringVarP(&buildOutput, "output", "o", "", "Output destination (e.g. type=image,push=true)")
	buildCmd.Flags().BoolVar(&buildSpot, "spot", false, "Use spot instances")
	buildCmd.Flags().StringVar(&buildInstanceType, "instance-type", "", "EC2 instance type")
	buildCmd.Flags().BoolVar(&buildKeep, "keep", false, "Keep workers running after build")
	buildCmd.Flags().DurationVar(&buildTimeout, "timeout", 0, "Build timeout")
	buildCmd.Flags().StringVar(&buildProgress, "progress", "auto", "Progress output type (auto, plain, tty)")
	buildCmd.Flags().BoolVar(&buildAutoUseExisting, "auto-use-existing", true, "Automatically detect and use existing workers (default: true)")
	buildCmd.Flags().StringVar(&buildProvenance, "provenance", "", "Generate provenance attestation for the build (e.g., mode=max)")

	// Bind flags to viper
	viper.BindPFlag("build.platforms", buildCmd.Flags().Lookup("platform"))
	viper.BindPFlag("build.push", buildCmd.Flags().Lookup("push"))
	viper.BindPFlag("build.load", buildCmd.Flags().Lookup("load"))
	viper.BindPFlag("build.spot", buildCmd.Flags().Lookup("spot"))
	viper.BindPFlag("build.keep", buildCmd.Flags().Lookup("keep"))
}

func runBuild(cmd *cobra.Command, args []string) error {
	// Get build context
	buildContext := "."
	if len(args) > 0 {
		buildContext = args[0]
	}

	// Resolve absolute path
	absContext, err := filepath.Abs(buildContext)
	if err != nil {
		return fmt.Errorf("failed to resolve build context: %w", err)
	}

	// Check if context exists
	if _, err := os.Stat(absContext); err != nil {
		return fmt.Errorf("build context does not exist: %s", absContext)
	}

	// Check if Dockerfile exists
	dockerfilePath := filepath.Join(absContext, buildFile)
	if _, err := os.Stat(dockerfilePath); err != nil {
		return fmt.Errorf("Dockerfile not found: %s", dockerfilePath)
	}

	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Override config with command-line flags
	if buildInstanceType != "" {
		cfg.AWS.InstanceType = buildInstanceType
	}
	if buildSpot {
		cfg.AWS.UseSpot = true
	}
	if buildTimeout > 0 {
		cfg.Timeouts.BuildTimeout = buildTimeout
	}

	// Determine platforms
	platforms := buildPlatforms
	if len(platforms) == 0 {
		platforms = cfg.Buildx.Platforms
	}

	// Apply platform-specific overrides for command-line flags
	if buildInstanceType != "" {
		// Also set for all platforms if no platform-specific config exists
		if cfg.AWS.Platforms == nil {
			cfg.AWS.Platforms = make(map[string]*config.PlatformConfig)
		}
		for _, platform := range platforms {
			if _, exists := cfg.AWS.Platforms[platform]; !exists {
				cfg.AWS.Platforms[platform] = &config.PlatformConfig{
					InstanceType: buildInstanceType,
				}
			} else if cfg.AWS.Platforms[platform].InstanceType == "" {
				cfg.AWS.Platforms[platform].InstanceType = buildInstanceType
			}
		}
	}
	if buildSpot {
		// Also set for all platforms if no platform-specific config exists
		if cfg.AWS.Platforms == nil {
			cfg.AWS.Platforms = make(map[string]*config.PlatformConfig)
		}
		for _, platform := range platforms {
			if _, exists := cfg.AWS.Platforms[platform]; !exists {
				cfg.AWS.Platforms[platform] = &config.PlatformConfig{
					UseSpot: &buildSpot,
				}
			} else if cfg.AWS.Platforms[platform].UseSpot == nil {
				useSpotPtr := buildSpot
				cfg.AWS.Platforms[platform].UseSpot = &useSpotPtr
			}
		}
	}

	// Validate build parameters
	if buildPush && buildLoad {
		return fmt.Errorf("cannot use both --push and --load")
	}

	if len(platforms) > 1 && buildLoad {
		return fmt.Errorf("cannot use --load with multiple platforms")
	}

	logrus.WithFields(logrus.Fields{
		"context":   absContext,
		"platforms": platforms,
		"tags":      buildTags,
		"keep":      buildKeep,
	}).Info("Starting build with on-demand workers")

	// Create managers
	ec2Manager, err := aws.NewEC2Manager(cfg)
	if err != nil {
		return fmt.Errorf("failed to create EC2 manager: %w", err)
	}

	buildxManager, err := buildx.NewManager(cfg)
	if err != nil {
		return fmt.Errorf("failed to create buildx manager: %w", err)
	}

	// Load state
	state, err := config.LoadState()
	if err != nil {
		return fmt.Errorf("failed to load state: %w", err)
	}

	// Find existing workers if auto-detection is enabled
	var existingWorkers []*config.WorkerState
	var missingPlatforms []string

	if buildAutoUseExisting {
		existingWorkers, missingPlatforms = findExistingWorkers(state, platforms)
		if len(existingWorkers) > 0 {
			logrus.WithFields(logrus.Fields{
				"existing": len(existingWorkers),
				"missing":  len(missingPlatforms),
				"total":    len(platforms),
			}).Info("Found existing workers")

			fmt.Printf("🔄 Using %d existing worker(s), launching %d new worker(s)\n",
				len(existingWorkers), len(missingPlatforms))
		}
	} else {
		missingPlatforms = platforms
	}

	// Generate unique build ID and builder name for temporary workers
	buildID := fmt.Sprintf("%d", time.Now().Unix())
	var temporaryBuilderName string
	var permanentBuilderName string

	logrus.Info("Starting build worker setup process")

	ctx := context.Background()
	var workers []*config.WorkerState
	var newWorkers []*config.WorkerState

	// Add existing workers to the list
	workers = append(workers, existingWorkers...)

	// Initialize builder names
	temporaryBuilderName = config.GenerateTemporaryBuilderName()
	permanentBuilderName = config.GetPermanentBuilderName()

	// Cleanup function - only cleanup newly launched workers
	defer func() {
		if !buildKeep && len(newWorkers) > 0 {
			logrus.WithField("worker_count", len(newWorkers)).Info("Starting cleanup sequence for new workers")

			// Phase 1: Remove workers from buildx and clean Docker contexts
			logrus.Info("Phase 1: Removing workers from buildx and cleaning Docker contexts")
			for _, worker := range newWorkers {
				logrus.WithFields(logrus.Fields{
					"worker":   worker.Name,
					"platform": worker.Platform,
				}).Info("Removing worker from buildx")
				if err := buildxManager.RemoveWorker(ctx, worker); err != nil {
					logrus.WithError(err).WithField("worker", worker.Name).Warn("Failed to remove worker from buildx")
				}
			}

			// Phase 2: Clean up temporary builder if it exists
			if temporaryBuilderName != "" {
				logrus.WithField("builder", temporaryBuilderName).Info("Phase 2: Removing temporary builder")
				if err := buildxManager.CleanupBuilder(ctx, temporaryBuilderName); err != nil {
					logrus.WithError(err).WithField("builder", temporaryBuilderName).Warn("Failed to cleanup temporary builder")
				}
			}

			// Phase 3: Terminate EC2 instances
			logrus.Info("Phase 3: Terminating EC2 instances")
			for _, worker := range newWorkers {
				logrus.WithFields(logrus.Fields{
					"instance_id": worker.InstanceID,
					"worker":      worker.Name,
				}).Info("Terminating instance")
				if err := ec2Manager.TerminateInstance(ctx, worker.InstanceID); err != nil {
					logrus.WithError(err).WithFields(logrus.Fields{
						"instance_id": worker.InstanceID,
						"worker":      worker.Name,
					}).Warn("Failed to terminate instance")
				}
			}
			logrus.Info("Cleanup sequence completed")
		}
		logrus.Info("Build worker setup process completed")
	}()

	// Launch workers for missing platforms only
	if len(missingPlatforms) > 0 {
		fmt.Printf("🚀 Launching %d new worker(s) for missing platforms: %v\n",
			len(missingPlatforms), missingPlatforms)
	}

	for _, platform := range missingPlatforms {
		workerName := config.GenerateTemporaryWorkerName(buildID, platform)

		logrus.WithField("platform", platform).Info("Launching worker")

		// Get platform-specific configuration
		useSpot := cfg.GetUseSpotForPlatform(platform)
		instanceType := cfg.GetInstanceTypeForPlatform(platform)

		logrus.WithFields(logrus.Fields{
			"worker":        workerName,
			"platform":      platform,
			"instance_type": instanceType,
			"use_spot":      useSpot,
		}).Info("Launching build worker with platform-specific configuration")

		// Launch instance
		instanceInfo, err := ec2Manager.LaunchInstance(ctx, workerName, platform, useSpot)
		if err != nil {
			return fmt.Errorf("failed to launch instance for %s: %w", platform, err)
		}

		// Create worker state
		worker := &config.WorkerState{
			Name:         workerName,
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
			BuilderName:  temporaryBuilderName,
		}

		workers = append(workers, worker)
		newWorkers = append(newWorkers, worker)

		// Setup worker

		if err := setupWorker(ctx, cfg, worker); err != nil {
			return fmt.Errorf("failed to setup worker %s: %w", workerName, err)
		}

		worker.Status = "running"
		worker.UpdatedAt = time.Now()

		// Add to state if keeping workers
		if buildKeep {
			state.AddWorker(worker)
		}

		logrus.WithFields(logrus.Fields{
			"worker":   workerName,
			"platform": platform,
			"ip":       worker.PublicIP,
		}).Info("Worker ready")
	}

	// Save state if keeping workers or using existing ones
	if buildKeep || len(existingWorkers) > 0 {
		if err := config.SaveState(state); err != nil {
			logrus.WithError(err).Warn("Failed to save state")
		}
	}

	// Create buildx builder and add workers
	logrus.Info("Creating buildx builder")

	// Determine the appropriate builder name based on worker types
	var activeBuilderName string
	if len(existingWorkers) > 0 && existingWorkers[0].IsPermanentWorker() {
		// Use permanent builder for permanent workers
		activeBuilderName = permanentBuilderName
		// Update permanent workers to use permanent builder if needed
		for _, worker := range existingWorkers {
			if worker.BuilderName != permanentBuilderName {
				worker.BuilderName = permanentBuilderName
				worker.UpdatedAt = time.Now()
			}
		}
	} else {
		// Use temporary builder for temporary workers or mixed scenarios
		activeBuilderName = temporaryBuilderName
	}

	for _, worker := range workers {
		status := "new"
		if contains(existingWorkers, worker) {
			status = "existing"
		}
		logrus.WithFields(logrus.Fields{
			"worker":   worker.Name,
			"platform": worker.Platform,
			"status":   status,
		}).Info("Adding worker to builder")

		// For mixed scenarios, temporary workers might need to use permanent builder
		if activeBuilderName == permanentBuilderName && worker.IsTemporaryWorker() {
			worker.BuilderName = permanentBuilderName
		}

		if err := buildxManager.AddWorker(ctx, worker); err != nil {
			return fmt.Errorf("failed to add worker to buildx: %w", err)
		}
	}

	// Bootstrap the builder to initialize it
	logrus.WithField("builder", activeBuilderName).Info("Bootstrapping buildx builder")
	if err := buildxManager.BootstrapBuilder(ctx, activeBuilderName); err != nil {
		logrus.WithError(err).WithField("builder", activeBuilderName).Warn("Failed to bootstrap builder")
		return fmt.Errorf("failed to bootstrap builder %s: %w", activeBuilderName, err)
	}

	// Prepare build command
	logrus.Info("Starting build")

	buildArgs := prepareBuildArgs(absContext, platforms, activeBuilderName)

	// Execute build
	if len(existingWorkers) > 0 && len(missingPlatforms) == 0 {
		fmt.Printf("\n🚀 Starting multi-platform build using existing workers...\n\n")
	} else if len(existingWorkers) > 0 {
		fmt.Printf("\n🚀 Starting multi-platform build using %d existing + %d new workers...\n\n",
			len(existingWorkers), len(missingPlatforms))
	} else {
		fmt.Printf("\n🚀 Starting multi-platform build with new workers...\n\n")
	}

	if err := executeBuild(ctx, cfg, buildArgs); err != nil {
		return fmt.Errorf("build failed: %w", err)
	}

	fmt.Printf("\n✅ Build completed successfully!\n")

	// Show worker summary
	fmt.Printf("\nWorker Summary:\n")
	for _, worker := range workers {
		status := "new"
		if contains(existingWorkers, worker) {
			status = "existing"
		}
		fmt.Printf("  • %s (%s) - %s [%s]\n", worker.Name, worker.Platform, worker.PublicIP, status)
	}

	if buildKeep {
		fmt.Printf("\nAll workers are kept running. Use 'buildex stop --all' to terminate them.\n")
	} else if len(existingWorkers) > 0 && len(missingPlatforms) > 0 {
		fmt.Printf("\n%d existing worker(s) remain running. %d new worker(s) will be terminated automatically.\n",
			len(existingWorkers), len(missingPlatforms))
	} else if len(existingWorkers) > 0 {
		fmt.Printf("\nAll workers were existing and remain running. Use 'buildex stop --all' to terminate them.\n")
	} else {
		fmt.Printf("\nAll workers will be terminated automatically.\n")
	}

	return nil
}

func prepareBuildArgs(context string, platforms []string, builderName string) []string {
	args := []string{"docker", "buildx", "build", "--builder", builderName}

	// Add context
	args = append(args, context)

	// Add platforms
	if len(platforms) > 0 {
		args = append(args, "--platform", strings.Join(platforms, ","))
	}

	// Add tags
	for _, tag := range buildTags {
		args = append(args, "--tag", tag)
	}

	// Add file
	if buildFile != "" {
		args = append(args, "--file", buildFile)
	}

	// Add target
	if buildTarget != "" {
		args = append(args, "--target", buildTarget)
	}

	// Add build args
	for _, arg := range buildArgs {
		args = append(args, "--build-arg", arg)
	}

	// Add secrets
	for _, secret := range buildSecrets {
		args = append(args, "--secret", secret)
	}

	// Add output options
	if buildPush {
		args = append(args, "--push")
	}
	if buildLoad {
		args = append(args, "--load")
	}
	if buildOutput != "" {
		args = append(args, "--output", buildOutput)
	}

	// Add progress
	args = append(args, "--progress", buildProgress)

	// Add provenance
	if buildProvenance != "" {
		args = append(args, "--provenance", buildProvenance)
	}

	// Add build contexts
	for _, context := range buildContexts {
		args = append(args, "--build-context", context)
	}

	return args
}

func executeBuild(ctx context.Context, cfg *config.Config, args []string) error {
	// Add timeout context if specified
	if cfg.Timeouts.BuildTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cfg.Timeouts.BuildTimeout)
		defer cancel()
	}

	// Execute buildx command
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	logrus.WithField("command", strings.Join(args, " ")).Debug("Executing build command")

	return cmd.Run()
}

// findExistingWorkers finds existing workers that match the required platforms
// Prioritizes permanent workers over temporary ones
func findExistingWorkers(state *config.GlobalState, platforms []string) ([]*config.WorkerState, []string) {
	var existingWorkers []*config.WorkerState
	var missingPlatforms []string

	// Create maps to track workers by platform, prioritizing permanent workers
	platformWorkers := make(map[string]*config.WorkerState)
	for _, worker := range state.Workers {
		if worker.Status != "running" {
			continue
		}

		// Always prefer permanent workers over temporary ones
		if existingWorker, exists := platformWorkers[worker.Platform]; exists {
			if worker.IsPermanentWorker() && !existingWorker.IsPermanentWorker() {
				platformWorkers[worker.Platform] = worker
			}
		} else {
			platformWorkers[worker.Platform] = worker
		}
	}

	// Check each required platform
	for _, platform := range platforms {
		if worker, exists := platformWorkers[platform]; exists {
			existingWorkers = append(existingWorkers, worker)
		} else {
			missingPlatforms = append(missingPlatforms, platform)
		}
	}

	return existingWorkers, missingPlatforms
}

// contains checks if a worker is in the slice
func contains(workers []*config.WorkerState, target *config.WorkerState) bool {
	for _, worker := range workers {
		if worker.Name == target.Name {
			return true
		}
	}
	return false
}
