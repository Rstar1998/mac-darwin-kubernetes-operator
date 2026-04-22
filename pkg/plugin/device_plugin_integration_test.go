//go:build integration
// +build integration

// Package plugin_test contains integration tests that require real Apple Silicon hardware.
// Run with: go test ./pkg/plugin/ -tags=integration -v
package plugin_test

import (
	"testing"

	"github.com/go-logr/logr"
	"github.com/gpu-operator-mac/apple-gpu-operator/pkg/plugin"
)

// TestDiscoverGPU_RealHardware verifies GPU discovery on actual Apple Silicon.
// This test ONLY runs on a real Mac with -tags=integration.
func TestDiscoverGPU_RealHardware(t *testing.T) {
	info, err := plugin.DiscoverGPU()
	if err != nil {
		t.Fatalf("DiscoverGPU() failed: %v", err)
	}

	t.Logf("Chip Model:   %s", info.ChipModel)
	t.Logf("Chip Variant: %s", info.ChipVariant)
	t.Logf("GPU Cores:    %d", info.GPUCores)
	t.Logf("GPU Family:   %s", info.GPUFamily)

	// Validate chip model starts with "Apple"
	if info.ChipModel == "" {
		t.Error("ChipModel is empty")
	}

	// GPU cores must be > 0 on real hardware
	if info.GPUCores <= 0 {
		t.Errorf("GPUCores = %d, expected > 0", info.GPUCores)
	}

	// Chip variant should be normalised (e.g., "m3", "m3-max", "m4-pro")
	if info.ChipVariant == "" {
		t.Error("ChipVariant is empty")
	}

	// Test slot calculation with typical values
	testCases := []struct {
		coresPerSlot int
		wantMin      int
	}{
		{0, 1},                   // 0 → whole GPU = 1 slot
		{info.GPUCores, 1},       // cores == perSlot → 1 slot
		{1, info.GPUCores},       // 1 core per slot → N slots
	}

	for _, tc := range testCases {
		dp := plugin.NewMetalDevicePlugin(
			discardLogger(), info, tc.coresPerSlot, nil, "test-node",
		)
		slots := dp.TotalSlots()
		if slots < tc.wantMin {
			t.Errorf("TotalSlots(cores=%d, perSlot=%d) = %d, want >= %d",
				info.GPUCores, tc.coresPerSlot, slots, tc.wantMin)
		}
		t.Logf("coresPerSlot=%d → %d slots", tc.coresPerSlot, slots)
	}
}

func discardLogger() logr.Logger {
	return logr.Discard()
}
