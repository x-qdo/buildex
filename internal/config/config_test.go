package config

import (
	"testing"
	"time"
)

func TestWorkerTypeDetection(t *testing.T) {
	tests := []struct {
		name        string
		workerName  string
		isPermanent bool
		isTemporary bool
	}{
		{
			name:        "permanent worker",
			workerName:  "buildex-myapp-linux-amd64",
			isPermanent: true,
			isTemporary: false,
		},
		{
			name:        "temporary worker",
			workerName:  "build-1234567890-linux-amd64",
			isPermanent: false,
			isTemporary: true,
		},
		{
			name:        "legacy buildex worker should be permanent",
			workerName:  "buildex-worker-linux-arm64",
			isPermanent: true,
			isTemporary: false,
		},
		{
			name:        "random worker name",
			workerName:  "random-worker-name",
			isPermanent: false,
			isTemporary: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			worker := &WorkerState{Name: tt.workerName}

			if worker.IsPermanentWorker() != tt.isPermanent {
				t.Errorf("IsPermanentWorker() = %v, want %v", worker.IsPermanentWorker(), tt.isPermanent)
			}

			if worker.IsTemporaryWorker() != tt.isTemporary {
				t.Errorf("IsTemporaryWorker() = %v, want %v", worker.IsTemporaryWorker(), tt.isTemporary)
			}
		})
	}
}

func TestBuilderNameGeneration(t *testing.T) {
	t.Run("permanent builder name", func(t *testing.T) {
		name := GetPermanentBuilderName()
		expected := "buildex-permanent"
		if name != expected {
			t.Errorf("GetPermanentBuilderName() = %v, want %v", name, expected)
		}
	})
}

func TestPlatformSpecificConfig(t *testing.T) {
	cfg := &Config{
		AWS: AWSConfig{
			InstanceType: "m5.large",
			UseSpot:      false,
			AMI:          "ami-12345",
			VolumeSize:   20,
			Platforms: map[string]*PlatformConfig{
				"linux/arm64": {
					InstanceType: "m6g.large",
					UseSpot:      boolPtr(true),
					AMI:          "ami-arm64",
					VolumeSize:   intPtr(30),
				},
				"linux/amd64": {
					InstanceType: "m5.xlarge",
					// UseSpot not set, should use default
					// AMI not set, should use default
					VolumeSize: intPtr(40),
				},
			},
		},
	}

	t.Run("platform-specific instance type", func(t *testing.T) {
		// Test ARM64 platform
		instanceType := cfg.GetInstanceTypeForPlatform("linux/arm64")
		if instanceType != "m6g.large" {
			t.Errorf("GetInstanceTypeForPlatform(linux/arm64) = %v, want m6g.large", instanceType)
		}

		// Test AMD64 platform
		instanceType = cfg.GetInstanceTypeForPlatform("linux/amd64")
		if instanceType != "m5.xlarge" {
			t.Errorf("GetInstanceTypeForPlatform(linux/amd64) = %v, want m5.xlarge", instanceType)
		}

		// Test unknown platform (should use default)
		instanceType = cfg.GetInstanceTypeForPlatform("linux/unknown")
		if instanceType != "m5.large" {
			t.Errorf("GetInstanceTypeForPlatform(linux/unknown) = %v, want m5.large", instanceType)
		}
	})

	t.Run("platform-specific spot instance usage", func(t *testing.T) {
		// Test ARM64 platform (has specific setting)
		useSpot := cfg.GetUseSpotForPlatform("linux/arm64")
		if !useSpot {
			t.Errorf("GetUseSpotForPlatform(linux/arm64) = %v, want true", useSpot)
		}

		// Test AMD64 platform (no specific setting, should use default)
		useSpot = cfg.GetUseSpotForPlatform("linux/amd64")
		if useSpot {
			t.Errorf("GetUseSpotForPlatform(linux/amd64) = %v, want false", useSpot)
		}

		// Test unknown platform (should use default)
		useSpot = cfg.GetUseSpotForPlatform("linux/unknown")
		if useSpot {
			t.Errorf("GetUseSpotForPlatform(linux/unknown) = %v, want false", useSpot)
		}
	})

	t.Run("platform-specific AMI", func(t *testing.T) {
		// Test ARM64 platform
		ami := cfg.GetAMIForPlatform("linux/arm64")
		if ami != "ami-arm64" {
			t.Errorf("GetAMIForPlatform(linux/arm64) = %v, want ami-arm64", ami)
		}

		// Test AMD64 platform (no specific AMI, should use default)
		ami = cfg.GetAMIForPlatform("linux/amd64")
		if ami != "ami-12345" {
			t.Errorf("GetAMIForPlatform(linux/amd64) = %v, want ami-12345", ami)
		}

		// Test unknown platform (should use default)
		ami = cfg.GetAMIForPlatform("linux/unknown")
		if ami != "ami-12345" {
			t.Errorf("GetAMIForPlatform(linux/unknown) = %v, want ami-12345", ami)
		}
	})

	t.Run("platform-specific volume size", func(t *testing.T) {
		// Test ARM64 platform
		volumeSize := cfg.GetVolumeSizeForPlatform("linux/arm64")
		if volumeSize != 30 {
			t.Errorf("GetVolumeSizeForPlatform(linux/arm64) = %v, want 30", volumeSize)
		}

		// Test AMD64 platform
		volumeSize = cfg.GetVolumeSizeForPlatform("linux/amd64")
		if volumeSize != 40 {
			t.Errorf("GetVolumeSizeForPlatform(linux/amd64) = %v, want 40", volumeSize)
		}

		// Test unknown platform (should use default)
		volumeSize = cfg.GetVolumeSizeForPlatform("linux/unknown")
		if volumeSize != 20 {
			t.Errorf("GetVolumeSizeForPlatform(linux/unknown) = %v, want 20", volumeSize)
		}
	})
}

// Helper functions for pointer creation
func boolPtr(b bool) *bool {
	return &b
}

func intPtr(i int) *int {
	return &i
}

func TestTemporaryBuilderName(t *testing.T) {
	t.Run("temporary builder name", func(t *testing.T) {
		name := GenerateTemporaryBuilderName()
		if !IsTemporaryBuilder(name) {
			t.Errorf("GenerateTemporaryBuilderName() = %v should be detected as temporary", name)
		}
		if IsPermanentBuilder(name) {
			t.Errorf("GenerateTemporaryBuilderName() = %v should not be detected as permanent", name)
		}
	})
}

func TestWorkerNameGeneration(t *testing.T) {
	t.Run("temporary worker name", func(t *testing.T) {
		buildID := "1234567890"
		platform := "linux/amd64"
		name := GenerateTemporaryWorkerName(buildID, platform)
		expected := "build-1234567890-linux-amd64"

		if name != expected {
			t.Errorf("GenerateTemporaryWorkerName() = %v, want %v", name, expected)
		}

		worker := &WorkerState{Name: name}
		if !worker.IsTemporaryWorker() {
			t.Errorf("Generated temporary worker name should be detected as temporary")
		}
	})

	t.Run("permanent worker name", func(t *testing.T) {
		baseName := "myapp"
		platform := "linux/arm64"
		name := GeneratePermanentWorkerName(baseName, platform)
		expected := "buildex-myapp-linux-arm64"

		if name != expected {
			t.Errorf("GeneratePermanentWorkerName() = %v, want %v", name, expected)
		}

		worker := &WorkerState{Name: name}
		if !worker.IsPermanentWorker() {
			t.Errorf("Generated permanent worker name should be detected as permanent")
		}
	})
}

func TestSanitizePlatform(t *testing.T) {
	tests := []struct {
		platform string
		expected string
	}{
		{"linux/amd64", "linux-amd64"},
		{"linux/arm64", "linux-arm64"},
		{"windows/amd64", "windows-amd64"},
		{"darwin/arm64", "darwin-arm64"},
		{"linux/arm/v7", "linux-arm-v7"},
	}

	for _, tt := range tests {
		t.Run(tt.platform, func(t *testing.T) {
			result := SanitizePlatform(tt.platform)
			if result != tt.expected {
				t.Errorf("SanitizePlatform(%v) = %v, want %v", tt.platform, result, tt.expected)
			}
		})
	}
}

func TestBuilderTypeDetection(t *testing.T) {
	tests := []struct {
		name        string
		builderName string
		isPermanent bool
		isTemporary bool
	}{
		{
			name:        "permanent builder",
			builderName: "buildex-permanent",
			isPermanent: true,
			isTemporary: false,
		},
		{
			name:        "temporary builder",
			builderName: "buildex-build-1234567890",
			isPermanent: false,
			isTemporary: true,
		},
		{
			name:        "legacy builder",
			builderName: "buildex-legacy",
			isPermanent: false,
			isTemporary: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if IsPermanentBuilder(tt.builderName) != tt.isPermanent {
				t.Errorf("IsPermanentBuilder(%v) = %v, want %v", tt.builderName, IsPermanentBuilder(tt.builderName), tt.isPermanent)
			}

			if IsTemporaryBuilder(tt.builderName) != tt.isTemporary {
				t.Errorf("IsTemporaryBuilder(%v) = %v, want %v", tt.builderName, IsTemporaryBuilder(tt.builderName), tt.isTemporary)
			}
		})
	}
}

func TestGlobalStateWorkerManagement(t *testing.T) {
	state := &GlobalState{
		Workers: make(map[string]*WorkerState),
	}

	// Add permanent workers
	permanentWorker1 := &WorkerState{
		Name:     "buildex-app1-linux-amd64",
		Status:   "running",
		Platform: "linux/amd64",
	}
	permanentWorker2 := &WorkerState{
		Name:     "buildex-app1-linux-arm64",
		Status:   "running",
		Platform: "linux/arm64",
	}

	// Add temporary workers
	temporaryWorker1 := &WorkerState{
		Name:     "build-1234567890-linux-amd64",
		Status:   "running",
		Platform: "linux/amd64",
	}
	temporaryWorker2 := &WorkerState{
		Name:     "build-1234567890-windows-amd64",
		Status:   "running",
		Platform: "windows/amd64",
	}

	state.AddWorker(permanentWorker1)
	state.AddWorker(permanentWorker2)
	state.AddWorker(temporaryWorker1)
	state.AddWorker(temporaryWorker2)

	t.Run("get permanent workers", func(t *testing.T) {
		permanentWorkers := state.GetPermanentWorkers()
		if len(permanentWorkers) != 2 {
			t.Errorf("Expected 2 permanent workers, got %d", len(permanentWorkers))
		}
	})

	t.Run("get temporary workers", func(t *testing.T) {
		temporaryWorkers := state.GetTemporaryWorkers()
		if len(temporaryWorkers) != 2 {
			t.Errorf("Expected 2 temporary workers, got %d", len(temporaryWorkers))
		}
	})

	t.Run("cleanup temporary workers", func(t *testing.T) {
		// Should have 4 workers total before cleanup
		if len(state.Workers) != 4 {
			t.Errorf("Expected 4 total workers before cleanup, got %d", len(state.Workers))
		}

		state.CleanupTemporaryWorkers()

		// Should have 2 workers left after cleanup (only permanent ones)
		if len(state.Workers) != 2 {
			t.Errorf("Expected 2 workers after cleanup, got %d", len(state.Workers))
		}

		// Verify only permanent workers remain
		for _, worker := range state.Workers {
			if !worker.IsPermanentWorker() {
				t.Errorf("Expected only permanent workers to remain, but found: %s", worker.Name)
			}
		}
	})
}

func TestWorkerPriority(t *testing.T) {
	state := &GlobalState{
		Workers: make(map[string]*WorkerState),
	}

	// Add both permanent and temporary workers for the same platform
	permanentWorker := &WorkerState{
		Name:      "buildex-app1-linux-amd64",
		Status:    "running",
		Platform:  "linux/amd64",
		CreatedAt: time.Now().Add(-1 * time.Hour), // Created earlier
	}
	temporaryWorker := &WorkerState{
		Name:      "build-1234567890-linux-amd64",
		Status:    "running",
		Platform:  "linux/amd64",
		CreatedAt: time.Now(), // Created more recently
	}

	state.AddWorker(temporaryWorker) // Add temporary first
	state.AddWorker(permanentWorker) // Add permanent second

	// When finding existing workers for a platform, permanent should be preferred
	platforms := []string{"linux/amd64"}
	existingWorkers, missingPlatforms := findExistingWorkers(state, platforms)

	if len(existingWorkers) != 1 {
		t.Errorf("Expected 1 existing worker, got %d", len(existingWorkers))
	}

	if len(missingPlatforms) != 0 {
		t.Errorf("Expected 0 missing platforms, got %d", len(missingPlatforms))
	}

	if existingWorkers[0].Name != permanentWorker.Name {
		t.Errorf("Expected permanent worker to be selected, got %s", existingWorkers[0].Name)
	}
}

// Helper function for testing - simulate the findExistingWorkers logic
func findExistingWorkers(state *GlobalState, platforms []string) ([]*WorkerState, []string) {
	var existingWorkers []*WorkerState
	var missingPlatforms []string

	// Create maps to track workers by platform, prioritizing permanent workers
	platformWorkers := make(map[string]*WorkerState)
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
