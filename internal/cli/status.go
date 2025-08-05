package cli

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/x-qdo/buildex/internal/aws"
	"github.com/x-qdo/buildex/internal/buildx"
	"github.com/x-qdo/buildex/internal/config"
)

var statusCmd = &cobra.Command{
	Use:   "status [worker-name]",
	Short: "Show status of buildx workers",
	Long: `Show the status of buildx workers including their EC2 instances,
buildx builder information, and health status.

Examples:
  # Show all workers
  buildex status

  # Show specific worker
  buildex status my-worker

  # Show detailed information
  buildex status --verbose

  # Show only running workers
  buildex status --running

  # Show builders information
  buildex status --builders`,
	RunE: runStatus,
}

var (
	statusVerbose  bool
	statusRunning  bool
	statusBuilders bool
	statusRefresh  bool
)

func init() {
	rootCmd.AddCommand(statusCmd)

	statusCmd.Flags().BoolVarP(&statusVerbose, "verbose", "v", false, "Show detailed information")
	statusCmd.Flags().BoolVar(&statusRunning, "running", false, "Show only running workers")
	statusCmd.Flags().BoolVar(&statusBuilders, "builders", false, "Show builders information")
	statusCmd.Flags().BoolVar(&statusRefresh, "refresh", false, "Refresh status from AWS")

	// Bind flags to viper
	viper.BindPFlag("status.verbose", statusCmd.Flags().Lookup("verbose"))
	viper.BindPFlag("status.running", statusCmd.Flags().Lookup("running"))
	viper.BindPFlag("status.builders", statusCmd.Flags().Lookup("builders"))
	viper.BindPFlag("status.refresh", statusCmd.Flags().Lookup("refresh"))
}

func runStatus(cmd *cobra.Command, args []string) error {
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

	// Create managers
	ec2Manager, err := aws.NewEC2Manager(cfg)
	if err != nil {
		return fmt.Errorf("failed to create EC2 manager: %w", err)
	}

	buildxManager, err := buildx.NewManager(cfg)
	if err != nil {
		return fmt.Errorf("failed to create buildx manager: %w", err)
	}

	ctx := context.Background()

	// Refresh status from AWS if requested
	if statusRefresh {
		if err := refreshWorkerStatus(ctx, ec2Manager, state); err != nil {
			logrus.WithError(err).Warn("Failed to refresh worker status")
		}
	}

	// Filter workers
	workers := state.ListWorkers()
	if len(args) > 0 {
		// Show specific worker
		workerName := args[0]
		if worker, exists := state.GetWorker(workerName); exists {
			workers = []*config.WorkerState{worker}
		} else {
			// Try partial match
			var matchedWorkers []*config.WorkerState
			for _, worker := range workers {
				if strings.Contains(worker.Name, workerName) {
					matchedWorkers = append(matchedWorkers, worker)
				}
			}
			workers = matchedWorkers
		}
	}

	if statusRunning {
		var runningWorkers []*config.WorkerState
		for _, worker := range workers {
			if worker.Status == "running" {
				runningWorkers = append(runningWorkers, worker)
			}
		}
		workers = runningWorkers
	}

	// Sort workers by name
	sort.Slice(workers, func(i, j int) bool {
		return workers[i].Name < workers[j].Name
	})

	// Show workers status
	if len(workers) == 0 {
		fmt.Println("No workers found.")
	} else {
		showWorkersStatus(workers, statusVerbose)
	}

	// Show builders if requested
	if statusBuilders || statusVerbose {
		fmt.Println()
		if err := showBuildersStatus(ctx, buildxManager, state); err != nil {
			logrus.WithError(err).Warn("Failed to show builders status")
		}
	}

	return nil
}

func showWorkersStatus(workers []*config.WorkerState, verbose bool) {
	fmt.Printf("Workers (%d):\n", len(workers))

	if !verbose {
		// Show compact view
		fmt.Printf("%-20s %-15s %-15s %-12s %-10s %s\n",
			"NAME", "PLATFORM", "PUBLIC IP", "INSTANCE", "STATUS", "CREATED")
		fmt.Println(strings.Repeat("-", 90))

		for _, worker := range workers {
			status := getStatusIcon(worker.Status) + " " + worker.Status
			created := formatTime(worker.CreatedAt)

			fmt.Printf("%-20s %-15s %-15s %-12s %-10s %s\n",
				truncate(worker.Name, 20),
				truncate(worker.Platform, 15),
				worker.PublicIP,
				worker.InstanceType,
				status,
				created)
		}
	} else {
		// Show detailed view
		for i, worker := range workers {
			if i > 0 {
				fmt.Println()
			}
			showWorkerDetails(worker)
		}
	}
}

func showWorkerDetails(worker *config.WorkerState) {
	fmt.Printf("Worker: %s\n", worker.Name)
	fmt.Printf("  Status:       %s %s\n", getStatusIcon(worker.Status), worker.Status)
	fmt.Printf("  Platform:     %s\n", worker.Platform)
	fmt.Printf("  Instance ID:  %s\n", worker.InstanceID)
	fmt.Printf("  Instance Type: %s\n", worker.InstanceType)
	fmt.Printf("  Public IP:    %s\n", worker.PublicIP)
	fmt.Printf("  Private IP:   %s\n", worker.PrivateIP)
	fmt.Printf("  Region:       %s\n", worker.Region)
	fmt.Printf("  Spot:         %t\n", worker.IsSpot)
	fmt.Printf("  Builder:      %s\n", worker.BuilderName)
	fmt.Printf("  SSH Key:      %s\n", worker.SSHKeyPath)
	fmt.Printf("  Created:      %s\n", worker.CreatedAt.Format(time.RFC3339))
	fmt.Printf("  Updated:      %s\n", worker.UpdatedAt.Format(time.RFC3339))

	if len(worker.Tags) > 0 {
		fmt.Printf("  Tags:\n")
		for key, value := range worker.Tags {
			fmt.Printf("    %s: %s\n", key, value)
		}
	}
}

func showBuildersStatus(ctx context.Context, buildxManager *buildx.Manager, state *config.GlobalState) error {
	builders, err := buildxManager.ListBuilders(ctx)
	if err != nil {
		return fmt.Errorf("failed to list builders: %w", err)
	}

	// Filter buildex builders
	var buildexBuilders []string
	for _, builder := range builders {
		if strings.HasPrefix(builder, "buildex-") {
			buildexBuilders = append(buildexBuilders, builder)
		}
	}

	fmt.Printf("Builders (%d):\n", len(buildexBuilders))

	if len(buildexBuilders) == 0 {
		fmt.Println("  No buildex builders found.")
		return nil
	}

	for _, builderName := range buildexBuilders {
		info, err := buildxManager.InspectBuilder(ctx, builderName)
		if err != nil {
			fmt.Printf("  %s: Error inspecting builder\n", builderName)
			continue
		}

		fmt.Printf("  %s:\n", builderName)
		fmt.Printf("    Nodes: %d\n", len(info.Nodes))

		for _, node := range info.Nodes {
			status := "unknown"
			if node.Status != "" {
				status = node.Status
			}

			fmt.Printf("      - %s (%s) - %s\n", node.Name, node.Platform, status)
			if node.Endpoint != "" {
				fmt.Printf("        Endpoint: %s\n", node.Endpoint)
			}
		}
	}

	return nil
}

func refreshWorkerStatus(ctx context.Context, ec2Manager *aws.EC2Manager, state *config.GlobalState) error {
	workers := state.ListWorkers()

	for _, worker := range workers {
		if worker.InstanceID == "" {
			continue
		}

		info, err := ec2Manager.GetInstanceInfo(ctx, worker.InstanceID)
		if err != nil {
			logrus.WithError(err).WithField("worker", worker.Name).Warn("Failed to get instance info")
			worker.Status = "unknown"
			continue
		}

		// Update worker status based on instance state
		switch info.State {
		case "running":
			if worker.Status == "starting" || worker.Status == "stopped" {
				worker.Status = "running"
			}
		case "stopped", "stopping":
			worker.Status = "stopped"
		case "terminated", "terminating":
			worker.Status = "terminated"
		case "pending":
			worker.Status = "starting"
		default:
			worker.Status = info.State
		}

		// Update IP addresses
		worker.PublicIP = info.PublicIP
		worker.PrivateIP = info.PrivateIP
		worker.UpdatedAt = time.Now()

		state.AddWorker(worker)
	}

	// Save updated state
	return config.SaveState(state)
}

func getStatusIcon(status string) string {
	switch status {
	case "running":
		return "✅"
	case "starting", "pending":
		return "🟡"
	case "stopped", "stopping":
		return "🔴"
	case "terminated", "terminating":
		return "❌"
	case "failed", "error":
		return "❌"
	default:
		return "❓"
	}
}

func formatTime(t time.Time) string {
	now := time.Now()
	diff := now.Sub(t)

	switch {
	case diff < time.Minute:
		return "just now"
	case diff < time.Hour:
		return fmt.Sprintf("%dm ago", int(diff.Minutes()))
	case diff < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(diff.Hours()))
	case diff < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(diff.Hours()/24))
	default:
		return t.Format("2006-01-02")
	}
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
