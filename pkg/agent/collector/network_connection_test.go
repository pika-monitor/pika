package collector

import (
	"net"
	"runtime"
	"testing"

	"github.com/pika-monitor/pika/internal/protocol"
)

func TestNetworkConnectionCollectorCollectOnBSD(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "freebsd" {
		t.Skip("requires macOS or FreeBSD")
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	data, err := NewNetworkConnectionCollector().Collect()
	if err != nil {
		t.Fatalf("collect network connections: %v", err)
	}
	if data.Total == 0 || data.Listen == 0 {
		t.Fatalf("expected TCP listener in connection counts: %+v", data)
	}
}

func TestAddConnectionStatus(t *testing.T) {
	data := &protocol.NetworkConnectionData{}
	for _, status := range []string{"ESTABLISHED", "SYN_SENT", "SYN_RECV", "FIN_WAIT1", "FIN_WAIT2", "TIME_WAIT", "CLOSE", "CLOSE_WAIT", "LAST_ACK", "LISTEN", "CLOSING", "NONE"} {
		addConnectionStatus(data, status)
	}
	if data.Total != 12 || data.Established != 1 || data.SynSent != 1 || data.SynRecv != 1 || data.FinWait1 != 1 || data.FinWait2 != 1 || data.TimeWait != 1 || data.Close != 1 || data.CloseWait != 1 || data.LastAck != 1 || data.Listen != 1 || data.Closing != 1 {
		t.Fatalf("unexpected connection counts: %+v", data)
	}
}

func TestCounterRate(t *testing.T) {
	for _, test := range []struct {
		current, previous uint64
		elapsed           float64
		want              uint64
	}{
		{300, 100, 2, 100},
		{300, 100, 0.5, 400},
		{10, 100, 1, 0},
		{300, 100, 0, 0},
		{^uint64(0), 0, 0.5, ^uint64(0)},
	} {
		if got := counterRate(safeDelta(test.current, test.previous), test.elapsed); got != test.want {
			t.Fatalf("rate(%+v) = %d", test, got)
		}
	}
}
