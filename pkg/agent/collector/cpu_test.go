package collector

import (
	"errors"
	"testing"
)

func TestCPUUsagePrimesFirstSample(t *testing.T) {
	collector := NewCPUCollector()
	if _, err := collector.collectUsage(func() ([]float64, error) { return nil, nil }); !errors.Is(err, ErrNoData) || collector.sampled {
		t.Fatal("empty sample should not initialize the baseline")
	}
	load := func() ([]float64, error) { return []float64{42}, nil }
	if _, err := collector.collectUsage(load); !errors.Is(err, ErrNoData) {
		t.Fatalf("first sample should prime the baseline, got %v", err)
	}
	usage, err := collector.collectUsage(load)
	if err != nil || usage != 42 {
		t.Fatalf("usage=%v, error=%v", usage, err)
	}
	if _, err := collector.collectUsage(func() ([]float64, error) { return nil, nil }); !errors.Is(err, ErrNoData) {
		t.Fatalf("empty sample should not report zero usage, got %v", err)
	}
}

func TestCPUUsageRetriesBaselineAfterFailure(t *testing.T) {
	collector := NewCPUCollector()
	failure := errors.New("CPU unavailable")
	if _, err := collector.collectUsage(func() ([]float64, error) { return nil, failure }); !errors.Is(err, failure) {
		t.Fatalf("expected failure, got %v", err)
	}
	if collector.sampled {
		t.Fatal("failed sample initialized baseline")
	}
	if _, err := collector.collectUsage(func() ([]float64, error) { return []float64{42}, nil }); !errors.Is(err, ErrNoData) {
		t.Fatalf("first successful sample should prime the baseline, got %v", err)
	}
}
