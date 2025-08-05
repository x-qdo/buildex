package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/x-qdo/buildex/internal/aws"
	"github.com/x-qdo/buildex/internal/buildx"
	"github.com/x-qdo/buildex/internal/config"
)

var stopCmd = &cobra.Command{
	Use:   "stop [worker-name]",
	Short: "Stop buildx workers and terminate EC2 instances",
	Long: `Stop one or more buildx workers and terminate their EC2 instances.
Workers are removed from buildx builders before termination.

Examples:
  # Stop a specific worker
  buildex stop my-worker

  # Stop all workers
  buildex stop --all

  # Stop workers for specific platforms
  buildex stop --platform linux/amd64,linux/arm64

  # Stop workers but keep state saved
  buildex stop --save-state

  # Force stop without confirmation
  buildex stop --all --force`,
	RunE: runStop,
}

var (
	stopAll       bool
	stopPlatforms []string
	stopSaveState bool
	stopForce     bool
)

func init() {
	rootCmd.AddCommand(stopCmd)

	stopCmd.Flags().BoolVar(&stopAll, "all", false, "Stop all workers")
	stopCmd.Flags().StringSliceVarP(&stopPlatforms, "platform", "p", nil, "Stop workers for specific platforms")
	stopCmd.Flags().BoolVar(&stopSaveState, "save-state", false, "Save state before stopping (keeps worker info but marks as stopped)")
	stopCmd.Flags().BoolVar(&stopForce, "force", false, "Force stop without confirmation")

	// Bind flags to viper
	viper.BindPFlag("stop.all", stopCmd.Flags().Lookup("all"))
	viper.BindPFlag("stop.platforms", stopCmd.Flags().Lookup("platform"))
	viper.BindPFlag("stop.save_state", stopCmd.Flags().Lookup("save-state"))
	viper.BindPFlag("stop.force", stopCmd.Flags().Lookup("force"))
}

func runStop(cmd *cobra.Command, args []string) error {
	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Load state
	state, err := config.LoadState()
	if err != nil {
		return fmt.Errorf("failed to load state: %w", err)
	}

	// Determine which workers to stop
	var workersToStop []*config.WorkerState

	if stopAll {
		workersToStop = state.ListWorkers()
	} else if len(stopPlatforms) > 0 {
		// Stop workers for specific platforms
		for _, worker := range state.ListWorkers() {
			for _, platform := range stopPlatforms {
				if strings.Contains(worker.Platform, platform) ||
					strings.Contains(worker.Name, config.SanitizePlatform(platform)) {
					workersToStop = append(workersToStop, worker)
					break
				}
			}
		}
	} else if len(args) > 0 {
		// Stop specific worker
		workerName := args[0]
		if worker, exists := state.GetWorker(workerName); exists {
			workersToStop = append(workersToStop, worker)
		} else {
			// Try to find workers by partial name match
			for _, worker := range state.ListWorkers() {
				if strings.Contains(worker.Name, workerName) {
					workersToStop = append(workersToStop, worker)
				}
			}
		}
	} else {
		return fmt.Errorf("specify worker name, --all, or --platform")
	}

	if len(workersToStop) == 0 {
		fmt.Println("No workers found to stop.")
		return nil
	}

	// Show confirmation unless force is used
	if !stopForce {
		fmt.Printf("The following workers will be stopped and their EC2 instances terminated:\n\n")
		for _, worker := range workersToStop {
			fmt.Printf("  • %s (%s) - %s - Instance: %s\n",
				worker.Name, worker.Platform, worker.PublicIP, worker.InstanceID)
		}
		fmt.Printf("\nThis action cannot be undone. Continue? (y/N): ")

		var response string
		fmt.Scanln(&response)
		if !strings.HasPrefix(strings.ToLower(response), "y") {
			fmt.Println("Operation cancelled.")
			return nil
		}
	}

	logrus.WithField("count", len(workersToStop)).Info("Stopping workers")

	logrus.WithFields(logrus.Fields{
		"workers": func() []string {
			names := make([]string, len(workersToStop))
			for i, w := range workersToStop {
				names[i] = w.Name
			}
			return names
		}(),
	}).Debug("Workers selected for stopping")

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

	logrus.Info("Starting worker shutdown process")

	ctx := context.Background()
	successCount := 0
	errors := make([]error, 0)

	for _, worker := range workersToStop {

		logrus.WithFields(logrus.Fields{
			"worker":      worker.Name,
			"instance_id": worker.InstanceID,
			"status":      worker.Status,
			"platform":    worker.Platform,
		}).Debug("Starting worker shutdown process")

		// Remove worker from buildx first
		if worker.Status == "running" {
			logrus.WithField("worker", worker.Name).Debug("Removing worker from buildx builder")
			if err := buildxManager.RemoveWorker(ctx, worker); err != nil {
				logrus.WithError(err).WithField("worker", worker.Name).Warn("Failed to remove worker from buildx")
				// Continue anyway to terminate the instance
			} else {
				logrus.WithField("worker", worker.Name).Debug("Worker removed from buildx successfully")
			}
		} else {
			logrus.WithFields(logrus.Fields{
				"worker": worker.Name,
				"status": worker.Status,
			}).Debug("Skipping buildx removal for non-running worker")
		}

		// Terminate EC2 instance
		if worker.InstanceID != "" {
			logrus.WithFields(logrus.Fields{
				"worker":      worker.Name,
				"instance_id": worker.InstanceID,
			}).Debug("Terminating EC2 instance")

			if err := ec2Manager.TerminateInstance(ctx, worker.InstanceID); err != nil {
				logrus.WithError(err).WithField("worker", worker.Name).Error("Failed to terminate instance")
				errors = append(errors, fmt.Errorf("failed to terminate %s: %w", worker.Name, err))
				continue
			}

			logrus.WithFields(logrus.Fields{
				"worker":      worker.Name,
				"instance_id": worker.InstanceID,
			}).Debug("EC2 instance terminated successfully")
		} else {
			logrus.WithField("worker", worker.Name).Debug("No instance ID found, skipping EC2 termination")
		}

		// Update worker state
		if stopSaveState {
			logrus.WithField("worker", worker.Name).Debug("Saving worker state as stopped")
			worker.Status = "stopped"
			worker.UpdatedAt = time.Now()
			state.AddWorker(worker)
		} else {
			logrus.WithField("worker", worker.Name).Debug("Removing worker from state completely")
			// Remove worker from state completely
			state.RemoveWorker(worker.Name)
		}

		successCount++

		logrus.WithFields(logrus.Fields{
			"worker":      worker.Name,
			"instance_id": worker.InstanceID,
			"platform":    worker.Platform,
		}).Info("Worker stopped")
	}

	// Save state
	logrus.Debug("Saving updated state after worker shutdown")
	if err := config.SaveState(state); err != nil {
		logrus.WithError(err).Warn("Failed to save state")
	} else {
		logrus.Debug("State saved successfully")
	}

	logrus.Info("Worker shutdown process completed")

	// Report results
	if successCount == len(workersToStop) {
		fmt.Printf("Successfully stopped %d workers.\n", successCount)
	} else {
		fmt.Printf("Stopped %d/%d workers successfully.\n", successCount, len(workersToStop))

		if len(errors) > 0 {
			fmt.Println("\nErrors encountered:")
			for _, err := range errors {
				fmt.Printf("  • %v\n", err)
			}
		}
	}

	// Clean up empty builders
	if successCount > 0 {
		logrus.Debug("Starting cleanup of empty builders")

		if err := buildxManager.CleanupEmptyBuilders(ctx); err != nil {
			logrus.WithError(err).Warn("Failed to cleanup empty builders")
		} else {
			logrus.Debug("Empty builders cleanup completed")
		}
	}

	return nil
}
