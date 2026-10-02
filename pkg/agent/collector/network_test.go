package collector

import (
	"errors"
	"testing"
	"time"

	gopsutilNet "github.com/shirou/gopsutil/v4/net"
)

func TestNetworkInterfacesCache(t *testing.T) {
	collector := NewNetworkCollector(nil)
	now := time.Unix(100, 0)
	calls := 0
	load := func() (gopsutilNet.InterfaceStatList, error) {
		calls++
		return gopsutilNet.InterfaceStatList{{Name: "eth0", MTU: calls}}, nil
	}
	for second := 0; second <= 30; second++ {
		interfaces, err := collector.collectInterfaces(now.Add(time.Duration(second)*time.Second), load)
		if err != nil {
			t.Fatal(err)
		}
		wantCalls := 1
		if second == 30 {
			wantCalls = 2
		}
		if calls != wantCalls || len(interfaces) != 1 || interfaces[0].MTU != wantCalls {
			t.Fatalf("second %d: calls=%d, interfaces=%+v", second, calls, interfaces)
		}
	}
}

func TestNetworkInterfacesCacheEmptyResult(t *testing.T) {
	collector := NewNetworkCollector(nil)
	now := time.Unix(100, 0)
	calls := 0
	load := func() (gopsutilNet.InterfaceStatList, error) {
		calls++
		return nil, nil
	}
	for second := 0; second < 30; second++ {
		if _, err := collector.collectInterfaces(now.Add(time.Duration(second)*time.Second), load); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("empty interface list was not cached: calls=%d", calls)
	}
}

func TestNetworkInterfacesCacheRetriesAfterFailure(t *testing.T) {
	collector := NewNetworkCollector(nil)
	now := time.Unix(100, 0)
	failure := errors.New("interfaces unavailable")
	fail := func() (gopsutilNet.InterfaceStatList, error) { return nil, failure }
	load := func() (gopsutilNet.InterfaceStatList, error) {
		return gopsutilNet.InterfaceStatList{{Name: "eth0"}}, nil
	}
	if _, err := collector.collectInterfaces(now, fail); !errors.Is(err, failure) {
		t.Fatalf("expected initial failure, got %v", err)
	}
	if !collector.interfacesUpdatedAt.IsZero() {
		t.Fatal("failed initial load updated cache timestamp")
	}
	if _, err := collector.collectInterfaces(now, load); err != nil {
		t.Fatal(err)
	}
	refreshAt := now.Add(networkInterfaceRefreshInterval)
	if _, err := collector.collectInterfaces(refreshAt, fail); !errors.Is(err, failure) {
		t.Fatalf("expected refresh failure, got %v", err)
	}
	if !collector.interfacesUpdatedAt.Equal(now) || len(collector.interfaces) != 1 {
		t.Fatal("failed refresh replaced previous cache")
	}
	if _, err := collector.collectInterfaces(refreshAt.Add(time.Second), load); err != nil {
		t.Fatal(err)
	}
	if !collector.interfacesUpdatedAt.Equal(refreshAt.Add(time.Second)) {
		t.Fatal("refresh was not retried after failure")
	}
}
