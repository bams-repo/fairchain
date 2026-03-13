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
	"time"

	"github.com/bams-repo/fairchain/internal/protocol"
	"github.com/bams-repo/fairchain/internal/types"
)

// Peer represents a connected remote node.
type Peer struct {
	conn      net.Conn
	addr      string
	inbound   bool
	version   *protocol.VersionMsg
	magic     [4]byte
	reader    *bufio.Reader

	mu        sync.Mutex
	knownInv  map[types.Hash]struct{} // Inventory this peer already knows about.
	sendQueue chan outMsg

	done chan struct{}
}

type outMsg struct {
	cmd     string
	payload []byte
}

const (
	peerSendQueueSize = 256
	readTimeout       = 5 * time.Minute
	writeTimeout      = 30 * time.Second
)

// NewPeer wraps a connection into a Peer.
func NewPeer(conn net.Conn, inbound bool, magic [4]byte) *Peer {
	return &Peer{
		conn:      conn,
		addr:      conn.RemoteAddr().String(),
		inbound:   inbound,
		magic:     magic,
		reader:    bufio.NewReader(conn),
		knownInv:  make(map[types.Hash]struct{}),
		sendQueue: make(chan outMsg, peerSendQueueSize),
		done:      make(chan struct{}),
	}
}

// Addr returns the peer's remote address.
func (p *Peer) Addr() string { return p.addr }

// IsInbound returns true if the peer connected to us.
func (p *Peer) IsInbound() bool { return p.inbound }

// Version returns the peer's version message (nil if handshake not complete).
func (p *Peer) Version() *protocol.VersionMsg {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.version == nil {
		return nil
	}
	v := *p.version
	return &v
}

// SetVersion stores the peer's version info after handshake.
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

// SetStartHeightIfGreater updates the peer's advertised height monotonically.
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

// AddKnownInventory marks an inventory item as known by this peer.
func (p *Peer) AddKnownInventory(hash types.Hash) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.knownInv[hash] = struct{}{}
	// Bound the known inventory cache.
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

// HasKnownInventory checks if the peer already knows about an inventory item.
func (p *Peer) HasKnownInventory(hash types.Hash) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.knownInv[hash]
	return ok
}

// SendMessage queues a message for sending to the peer.
func (p *Peer) SendMessage(cmd string, payload []byte) {
	select {
	case p.sendQueue <- outMsg{cmd: cmd, payload: payload}:
	case <-p.done:
	default:
		log.Printf("[p2p] send queue full for peer %s, dropping message %s", p.addr, cmd)
	}
}

// WriteLoop processes the send queue and writes messages to the connection.
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
		case <-p.done:
			return
		}
	}
}

// ReadMessage reads the next message from the peer.
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

	// Verify checksum.
	expectedChecksum := doubleSHA256First4(payload)
	if !bytes.Equal(hdr.Checksum[:], expectedChecksum[:]) {
		return nil, nil, fmt.Errorf("checksum mismatch from %s", p.addr)
	}

	return hdr, payload, nil
}

// Close shuts down the peer connection.
func (p *Peer) Close() {
	select {
	case <-p.done:
	default:
		close(p.done)
	}
	p.conn.Close()
}

// Done returns a channel that is closed when the peer is disconnected.
func (p *Peer) Done() <-chan struct{} { return p.done }

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
