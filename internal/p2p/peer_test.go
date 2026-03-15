package p2p

import (
	"net"
	"testing"
	"time"

	"github.com/bams-repo/fairchain/internal/types"
)

func testMagic() [4]byte { return [4]byte{0xf9, 0xbe, 0xb4, 0xd9} }

func newTestPeer(inbound bool) (*Peer, net.Conn) {
	server, client := net.Pipe()
	var conn net.Conn
	if inbound {
		conn = server
	} else {
		conn = client
	}
	p := NewPeer(conn, inbound, testMagic())
	if inbound {
		return p, client
	}
	return p, server
}

func TestPeerLivenessTracking(t *testing.T) {
	p, remote := newTestPeer(false)
	defer p.Close()
	defer remote.Close()

	if p.ConnectedAt().IsZero() {
		t.Fatal("connectedAt should be set")
	}
	if p.LastRecv().IsZero() {
		t.Fatal("lastRecv should be initialized")
	}
	if p.LastSend().IsZero() {
		t.Fatal("lastSend should be initialized")
	}
}

func TestPeerPingPong(t *testing.T) {
	p, remote := newTestPeer(false)
	defer p.Close()
	defer remote.Close()

	nonce := uint64(12345)
	p.SetPingNonce(nonce)

	if p.PongOverdue() {
		t.Fatal("pong should not be overdue immediately after ping")
	}

	if p.HandlePong(99999) {
		t.Fatal("mismatched nonce should return false")
	}

	if !p.HandlePong(nonce) {
		t.Fatal("matching nonce should return true")
	}

	if p.PingLatency() == 0 {
		t.Fatal("ping latency should be non-zero after pong")
	}

	if p.PongOverdue() {
		t.Fatal("pong should not be overdue after successful pong")
	}
}

func TestPeerBanScore(t *testing.T) {
	p, remote := newTestPeer(true)
	defer p.Close()
	defer remote.Close()

	if p.BanScore() != 0 {
		t.Fatal("initial ban score should be 0")
	}

	p.AddBanScore(10)
	if p.BanScore() != 10 {
		t.Fatalf("expected 10, got %d", p.BanScore())
	}

	p.AddBanScore(50)
	if p.BanScore() != 60 {
		t.Fatalf("expected 60, got %d", p.BanScore())
	}

	p.AddBanScore(40)
	if p.BanScore() < BanThreshold {
		t.Fatalf("expected >= %d, got %d", BanThreshold, p.BanScore())
	}
}

func TestPeerRateLimit(t *testing.T) {
	p, remote := newTestPeer(false)
	defer p.Close()
	defer remote.Close()

	for i := 0; i < int(RateLimitMaxMsgs); i++ {
		if !p.CheckRateLimit() {
			t.Fatalf("should be within limit at message %d", i+1)
		}
	}

	if p.CheckRateLimit() {
		t.Fatal("should exceed rate limit")
	}
}

func TestPeerInventory(t *testing.T) {
	p, remote := newTestPeer(false)
	defer p.Close()
	defer remote.Close()

	h := types.Hash{1, 2, 3}
	if p.HasKnownInventory(h) {
		t.Fatal("should not know inventory initially")
	}

	p.AddKnownInventory(h)
	if !p.HasKnownInventory(h) {
		t.Fatal("should know inventory after adding")
	}
}

func TestPeerInfo(t *testing.T) {
	p, remote := newTestPeer(true)
	defer p.Close()
	defer remote.Close()

	info := p.Info()
	if !info.Inbound {
		t.Fatal("should be inbound")
	}
	if info.ConnectedAt == 0 {
		t.Fatal("connected_at should be set")
	}
}

func TestPongOverdueTimeout(t *testing.T) {
	p, remote := newTestPeer(false)
	defer p.Close()
	defer remote.Close()

	p.mu.Lock()
	p.pingNonce = 42
	p.pingSent = time.Now().Add(-PongTimeout - time.Second)
	p.mu.Unlock()

	if !p.PongOverdue() {
		t.Fatal("pong should be overdue after timeout")
	}
}

func TestExtractIP(t *testing.T) {
	tests := []struct {
		addr string
		want string
	}{
		{"192.168.1.1:8333", "192.168.1.1"},
		{"[::1]:8333", "::1"},
		{"192.168.1.1", "192.168.1.1"},
	}
	for _, tt := range tests {
		got := extractIP(tt.addr)
		if got != tt.want {
			t.Errorf("extractIP(%q) = %q, want %q", tt.addr, got, tt.want)
		}
	}
}
