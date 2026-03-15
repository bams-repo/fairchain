package p2p

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bams-repo/fairchain/internal/protocol"
	"github.com/bams-repo/fairchain/internal/types"
)

// Peer represents a connected remote node.
// Fields are grouped by function: identity, protocol state, liveness,
// misbehavior scoring, rate limiting, and inventory deduplication.
type Peer struct {
	conn    net.Conn
	addr    string
	inbound bool
	magic   [4]byte
	reader  *bufio.Reader

	mu      sync.Mutex
	version *protocol.VersionMsg

	// Liveness tracking (Bitcoin Core parity: BIP 31 ping/pong).
	connectedAt time.Time
	lastRecv    atomic.Int64 // unix timestamp of last message received
	lastSend    atomic.Int64 // unix timestamp of last message sent
	pingNonce   uint64       // nonce of the outstanding ping (0 = none pending)
	pingSent    time.Time    // when the outstanding ping was sent
	lastPong    time.Time    // when the last valid pong was received
	pingLatency time.Duration

	// Misbehavior scoring (Bitcoin Core parity: ban at 100).
	banScore int32 // atomic-style but guarded by mu for compound ops

	// Rate limiting: sliding window message counter.
	msgCount   int32     // messages received in current window
	windowStart time.Time // start of current rate-limit window

	// Address gossip: Bitcoin Core only responds to one getaddr per connection.
	getaddrResponded bool

	// Inventory deduplication.
	knownInv  map[types.Hash]struct{}
	sendQueue chan outMsg

	// Bytes transferred.
	bytesRecv atomic.Int64
	bytesSent atomic.Int64

	done chan struct{}
}

type outMsg struct {
	cmd     string
	payload []byte
}

const (
	peerSendQueueSize = 1024
	readTimeout       = 5 * time.Minute
	writeTimeout      = 30 * time.Second

	// Bitcoin Core parity: ping every 2 minutes.
	PingInterval = 2 * time.Minute
	// Bitcoin Core parity: disconnect if no pong within 20 minutes.
	PongTimeout = 20 * time.Minute

	// Misbehavior threshold — peer is banned when score reaches this.
	BanThreshold int32 = 100
	// Duration of an IP ban.
	BanDuration = 24 * time.Hour

	// Rate limiting: max messages per window.
	RateLimitWindow   = 10 * time.Second
	RateLimitMaxMsgs  = 500
)

// NewPeer wraps a connection into a Peer.
func NewPeer(conn net.Conn, inbound bool, magic [4]byte) *Peer {
	now := time.Now()
	p := &Peer{
		conn:        conn,
		addr:        conn.RemoteAddr().String(),
		inbound:     inbound,
		magic:       magic,
		reader:      bufio.NewReader(conn),
		connectedAt: now,
		lastPong:    now,
		windowStart: now,
		knownInv:    make(map[types.Hash]struct{}),
		sendQueue:   make(chan outMsg, peerSendQueueSize),
		done:        make(chan struct{}),
	}
	p.lastRecv.Store(now.Unix())
	p.lastSend.Store(now.Unix())
	return p
}

// --- Identity accessors ---

func (p *Peer) Addr() string    { return p.addr }
func (p *Peer) IsInbound() bool { return p.inbound }

func (p *Peer) Version() *protocol.VersionMsg {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.version == nil {
		return nil
	}
	v := *p.version
	return &v
}

func (p *Peer) SetVersion(v *protocol.VersionMsg) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if v == nil {
		p.version = nil
		return
	}
	cp := *v
	p.version = &cp
}

func (p *Peer) SetStartHeightIfGreater(height uint32) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.version == nil {
		return
	}
	if height > p.version.StartHeight {
		p.version.StartHeight = height
	}
}

// BestHeight returns the highest block height known for this peer,
// incorporating both the initial handshake height and any updates
// received via block relay.
func (p *Peer) BestHeight() uint32 {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.version == nil {
		return 0
	}
	return p.version.StartHeight
}

// --- Liveness ---

func (p *Peer) ConnectedAt() time.Time     { return p.connectedAt }
func (p *Peer) LastRecv() time.Time         { return time.Unix(p.lastRecv.Load(), 0) }
func (p *Peer) LastSend() time.Time         { return time.Unix(p.lastSend.Load(), 0) }
func (p *Peer) PingLatency() time.Duration  { return p.pingLatency }

func (p *Peer) stampRecv() { p.lastRecv.Store(time.Now().Unix()) }
func (p *Peer) stampSend() { p.lastSend.Store(time.Now().Unix()) }

// SetPingNonce records that a ping with the given nonce was sent.
func (p *Peer) SetPingNonce(nonce uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pingNonce = nonce
	p.pingSent = time.Now()
}

// HandlePong processes an incoming pong. Returns true if the nonce matched.
func (p *Peer) HandlePong(nonce uint64) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pingNonce == 0 || nonce != p.pingNonce {
		return false
	}
	p.pingLatency = time.Since(p.pingSent)
	p.lastPong = time.Now()
	p.pingNonce = 0
	return true
}

// PongOverdue returns true if a ping is outstanding and the pong timeout
// has elapsed — matching Bitcoin Core's 20-minute pong deadline.
func (p *Peer) PongOverdue() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pingNonce == 0 {
		return false
	}
	return time.Since(p.pingSent) > PongTimeout
}

// LastPong returns when the last valid pong was received.
func (p *Peer) LastPong() time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastPong
}

// --- Misbehavior scoring ---

// AddBanScore increases the peer's misbehavior score. Returns the new score.
func (p *Peer) AddBanScore(delta int32) int32 {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.banScore += delta
	return p.banScore
}

// BanScore returns the current misbehavior score.
func (p *Peer) BanScore() int32 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.banScore
}

// --- Rate limiting ---

// CheckRateLimit returns true if the peer is within acceptable message rates.
// Returns false if the peer is flooding (should be penalized/disconnected).
func (p *Peer) CheckRateLimit() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	if now.Sub(p.windowStart) > RateLimitWindow {
		p.msgCount = 0
		p.windowStart = now
	}
	p.msgCount++
	return p.msgCount <= int32(RateLimitMaxMsgs)
}

// --- Addr gossip ---

// MarkGetAddrResponded marks that we've responded to a getaddr from this peer.
// Returns true if this is the first getaddr (should respond), false if already responded.
func (p *Peer) MarkGetAddrResponded() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.getaddrResponded {
		return false
	}
	p.getaddrResponded = true
	return true
}

// --- Inventory ---

func (p *Peer) AddKnownInventory(hash types.Hash) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.knownInv[hash] = struct{}{}
	if len(p.knownInv) > 10000 {
		count := 0
		for k := range p.knownInv {
			delete(p.knownInv, k)
			count++
			if count > 2000 {
				break
			}
		}
	}
}

func (p *Peer) HasKnownInventory(hash types.Hash) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.knownInv[hash]
	return ok
}

// --- I/O ---

func (p *Peer) SendMessage(cmd string, payload []byte) {
	select {
	case p.sendQueue <- outMsg{cmd: cmd, payload: payload}:
	case <-p.done:
	default:
		log.Printf("[p2p] send queue full for peer %s, dropping message %s", p.addr, cmd)
	}
}

// TrySendMessage attempts a non-blocking send and returns true if the message
// was enqueued. Used by sync logic to detect full queues and try other peers.
func (p *Peer) TrySendMessage(cmd string, payload []byte) bool {
	select {
	case p.sendQueue <- outMsg{cmd: cmd, payload: payload}:
		return true
	case <-p.done:
		return false
	default:
		return false
	}
}

// SendQueue returns the underlying send channel for diagnostic inspection.
func (p *Peer) SendQueue() chan outMsg { return p.sendQueue }

// SendLowPriority sends a message only if the queue has plenty of headroom.
// Used for addr gossip and other non-consensus messages so they don't starve
// block relay and sync traffic when the queue is under pressure.
func (p *Peer) SendLowPriority(cmd string, payload []byte) {
	if len(p.sendQueue) > cap(p.sendQueue)/2 {
		return
	}
	select {
	case p.sendQueue <- outMsg{cmd: cmd, payload: payload}:
	case <-p.done:
	default:
	}
}

func (p *Peer) WriteLoop() {
	for {
		select {
		case msg := <-p.sendQueue:
			p.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
			if err := protocol.EncodeMessageHeader(p.conn, p.magic, msg.cmd, msg.payload); err != nil {
				log.Printf("[p2p] write header error to %s: %v", p.addr, err)
				p.Close()
				return
			}
			if _, err := p.conn.Write(msg.payload); err != nil {
				log.Printf("[p2p] write payload error to %s: %v", p.addr, err)
				p.Close()
				return
			}
			p.bytesSent.Add(int64(protocol.MessageHeaderSize + len(msg.payload)))
			p.stampSend()
		case <-p.done:
			return
		}
	}
}

func (p *Peer) ReadMessage() (*protocol.MessageHeader, []byte, error) {
	p.conn.SetReadDeadline(time.Now().Add(readTimeout))
	hdr, err := protocol.DecodeMessageHeader(p.reader)
	if err != nil {
		return nil, nil, fmt.Errorf("read header from %s: %w", p.addr, err)
	}

	if hdr.Magic != p.magic {
		return nil, nil, fmt.Errorf("bad magic from %s: got %x want %x", p.addr, hdr.Magic, p.magic)
	}

	payload := make([]byte, hdr.Length)
	if _, err := io.ReadFull(p.reader, payload); err != nil {
		return nil, nil, fmt.Errorf("read payload from %s: %w", p.addr, err)
	}

	expectedChecksum := doubleSHA256First4(payload)
	if !bytes.Equal(hdr.Checksum[:], expectedChecksum[:]) {
		return nil, nil, fmt.Errorf("checksum mismatch from %s", p.addr)
	}

	p.bytesRecv.Add(int64(protocol.MessageHeaderSize + len(payload)))
	p.stampRecv()
	return hdr, payload, nil
}

// --- Lifecycle ---

func (p *Peer) Close() {
	select {
	case <-p.done:
	default:
		close(p.done)
	}
	p.conn.Close()
}

func (p *Peer) Done() <-chan struct{} { return p.done }

// --- Info snapshot for RPC ---

type PeerInfo struct {
	Addr        string        `json:"addr"`
	Inbound     bool          `json:"inbound"`
	Version     uint32        `json:"version"`
	UserAgent   string        `json:"user_agent"`
	StartHeight uint32        `json:"start_height"`
	BanScore    int32         `json:"ban_score"`
	PingMs      int64         `json:"ping_ms"`
	ConnectedAt int64         `json:"connected_at"`
	LastRecv    int64         `json:"last_recv"`
	LastSend    int64         `json:"last_send"`
	BytesRecv   int64         `json:"bytes_recv"`
	BytesSent   int64         `json:"bytes_sent"`
}

func (p *Peer) Info() PeerInfo {
	info := PeerInfo{
		Addr:        p.addr,
		Inbound:     p.inbound,
		ConnectedAt: p.connectedAt.Unix(),
		LastRecv:    p.lastRecv.Load(),
		LastSend:    p.lastSend.Load(),
		BytesRecv:   p.bytesRecv.Load(),
		BytesSent:   p.bytesSent.Load(),
	}
	p.mu.Lock()
	info.BanScore = p.banScore
	info.PingMs = p.pingLatency.Milliseconds()
	if p.version != nil {
		info.Version = p.version.Version
		info.UserAgent = p.version.UserAgent
		info.StartHeight = p.version.StartHeight
	}
	p.mu.Unlock()
	return info
}

// --- Crypto helpers ---

func doubleSHA256First4(data []byte) [4]byte {
	first := sha256sum(data)
	second := sha256sum(first[:])
	var out [4]byte
	copy(out[:], second[:4])
	return out
}

func sha256sum(data []byte) [32]byte {
	return sha256.Sum256(data)
}
