package p2p

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/bams-repo/fairchain/internal/chain"
	"github.com/bams-repo/fairchain/internal/crypto"
	"github.com/bams-repo/fairchain/internal/logging"
	"github.com/bams-repo/fairchain/internal/mempool"
	"github.com/bams-repo/fairchain/internal/metrics"
	"github.com/bams-repo/fairchain/internal/params"
	"github.com/bams-repo/fairchain/internal/protocol"
	"github.com/bams-repo/fairchain/internal/store"
	"github.com/bams-repo/fairchain/internal/types"
	"github.com/bams-repo/fairchain/internal/version"
)

// TimeSampler accepts peer clock samples for network-adjusted time.
type TimeSampler interface {
	AddSample(addr string, peerTime int64)
}

// Manager handles peer connections, handshakes, and message routing.
type Manager struct {
	params      *params.ChainParams
	chain       *chain.Chain
	mempool     *mempool.Mempool
	peerStore   store.PeerStore
	listenAddr  string
	maxInbound  int
	maxOutbound int
	seedPeers   []string
	timeSampler TimeSampler

	mu             sync.RWMutex
	peers          map[string]*Peer
	localNonce     uint64
	bestPeerHeight uint32

	seenBlocks *boundedHashSet
	seenTxs    *boundedHashSet

	listener net.Listener
}

const (
	maxSeenBlocks = 10000
	maxSeenTxs    = 50000
)

// boundedHashSet is a bounded set of hashes with FIFO eviction, modeled after
// Bitcoin Core's CRollingBloomFilter used for inventory deduplication.
type boundedHashSet struct {
	mu    sync.Mutex
	items map[types.Hash]struct{}
	order []types.Hash
	cap   int
}

func newBoundedHashSet(capacity int) *boundedHashSet {
	return &boundedHashSet{
		items: make(map[types.Hash]struct{}, capacity),
		order: make([]types.Hash, 0, capacity),
		cap:   capacity,
	}
}

func (s *boundedHashSet) Add(h types.Hash) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.items[h]; ok {
		return
	}
	if len(s.items) >= s.cap {
		evict := s.order[0]
		s.order = s.order[1:]
		delete(s.items, evict)
	}
	s.items[h] = struct{}{}
	s.order = append(s.order, h)
}

func (s *boundedHashSet) Has(h types.Hash) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.items[h]
	return ok
}

func (s *boundedHashSet) AddOrHas(h types.Hash) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.items[h]; ok {
		return true
	}
	if len(s.items) >= s.cap {
		evict := s.order[0]
		s.order = s.order[1:]
		delete(s.items, evict)
	}
	s.items[h] = struct{}{}
	s.order = append(s.order, h)
	return false
}

func (s *boundedHashSet) Remove(h types.Hash) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.items, h)
}

// NewManager creates a new P2P manager. ts may be nil if no time adjustment is needed.
func NewManager(p *params.ChainParams, c *chain.Chain, mp *mempool.Mempool, ps store.PeerStore, listenAddr string, maxIn, maxOut int, seeds []string, ts TimeSampler) *Manager {
	var nonce uint64
	b := make([]byte, 8)
	rand.Read(b)
	nonce = binary.LittleEndian.Uint64(b)

	return &Manager{
		params:      p,
		chain:       c,
		mempool:     mp,
		peerStore:   ps,
		listenAddr:  listenAddr,
		maxInbound:  maxIn,
		maxOutbound: maxOut,
		seedPeers:   seeds,
		timeSampler: ts,
		peers:       make(map[string]*Peer),
		localNonce:  nonce,
		seenBlocks:  newBoundedHashSet(maxSeenBlocks),
		seenTxs:     newBoundedHashSet(maxSeenTxs),
	}
}

// Start begins listening for connections and connecting to seed peers.
func (m *Manager) Start(ctx context.Context) error {
	ln, err := net.Listen("tcp", m.listenAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", m.listenAddr, err)
	}
	m.listener = ln
	logging.L.Info("listening", "component", "p2p", "addr", m.listenAddr)

	go m.acceptLoop(ctx)
	go m.connectSeeds(ctx)
	go m.reconnectLoop(ctx)
	go m.syncLoop(ctx)

	return nil
}

// Stop shuts down the P2P manager.
func (m *Manager) Stop() {
	if m.listener != nil {
		m.listener.Close()
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, p := range m.peers {
		p.Close()
	}
}

// PeerCount returns the number of connected peers.
func (m *Manager) PeerCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.peers)
}

// IsSyncing returns true if the node's chain height is significantly behind
// the best known peer height, indicating initial block download is in progress.
func (m *Manager) IsSyncing() bool {
	_, ourHeight := m.chain.Tip()
	m.mu.RLock()
	best := m.bestPeerHeight
	m.mu.RUnlock()
	if best == 0 {
		return false
	}
	return ourHeight+5 < best
}

// BestPeerHeight returns the highest block height reported by any connected peer.
func (m *Manager) BestPeerHeight() uint32 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.bestPeerHeight
}

// PeerAddrs returns the addresses of all connected peers.
func (m *Manager) PeerAddrs() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	addrs := make([]string, 0, len(m.peers))
	for addr := range m.peers {
		addrs = append(addrs, addr)
	}
	return addrs
}

// BroadcastBlock announces a new block to all peers that don't already know it.
func (m *Manager) BroadcastBlock(hash types.Hash, block *types.Block) {
	m.seenBlocks.Add(hash)

	inv := protocol.InvMsg{
		Inventory: []protocol.InvVector{
			{Type: protocol.InvTypeBlock, Hash: hash},
		},
	}
	var buf bytes.Buffer
	inv.Encode(&buf)
	payload := buf.Bytes()

	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, p := range m.peers {
		if !p.HasKnownInventory(hash) {
			p.AddKnownInventory(hash)
			p.SendMessage(protocol.CmdInv, payload)
		}
	}
}

// BroadcastTx announces a new transaction to all peers.
func (m *Manager) BroadcastTx(hash types.Hash) {
	m.seenTxs.Add(hash)

	inv := protocol.InvMsg{
		Inventory: []protocol.InvVector{
			{Type: protocol.InvTypeTx, Hash: hash},
		},
	}
	var buf bytes.Buffer
	inv.Encode(&buf)
	payload := buf.Bytes()

	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, p := range m.peers {
		if !p.HasKnownInventory(hash) {
			p.AddKnownInventory(hash)
			p.SendMessage(protocol.CmdInv, payload)
		}
	}
}

func (m *Manager) acceptLoop(ctx context.Context) {
	for {
		conn, err := m.listener.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
				logging.L.Error("accept error", "component", "p2p", "error", err)
				continue
			}
		}

		m.mu.RLock()
		inboundCount := 0
		for _, p := range m.peers {
			if p.IsInbound() {
				inboundCount++
			}
		}
		m.mu.RUnlock()

		if inboundCount >= m.maxInbound {
			conn.Close()
			continue
		}

		peer := NewPeer(conn, true, m.params.NetworkMagic)
		go m.handlePeer(ctx, peer)
	}
}

func (m *Manager) connectSeeds(ctx context.Context) {
	allSeeds := append(m.seedPeers, m.params.SeedNodes...)

	// Also load persisted peers.
	if stored, err := m.peerStore.GetPeers(); err == nil {
		allSeeds = append(allSeeds, stored...)
	}

	for _, addr := range allSeeds {
		select {
		case <-ctx.Done():
			return
		default:
		}
		m.connectPeer(ctx, addr)
	}
}

func (m *Manager) reconnectLoop(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.mu.RLock()
			peerCount := len(m.peers)
			connected := make(map[string]bool, peerCount)
			for addr := range m.peers {
				connected[addr] = true
			}
			m.mu.RUnlock()

			if peerCount >= m.maxOutbound {
				continue
			}

			// Priority 1: Always try seed peers — they're the network backbone.
			allSeeds := append(m.seedPeers, m.params.SeedNodes...)
			for _, addr := range allSeeds {
				if !connected[addr] {
					m.connectPeer(ctx, addr)
					connected[addr] = true
				}
			}

			// Priority 2: Try stored peers if still under limit.
			if stored, err := m.peerStore.GetPeers(); err == nil {
				for _, addr := range stored {
					if !connected[addr] {
						m.connectPeer(ctx, addr)
						break
					}
				}
			}
		}
	}
}

func (m *Manager) connectPeer(ctx context.Context, addr string) {
	m.mu.RLock()
	if _, exists := m.peers[addr]; exists {
		m.mu.RUnlock()
		return
	}
	m.mu.RUnlock()

	conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		return
	}

	peer := NewPeer(conn, false, m.params.NetworkMagic)
	go m.handlePeer(ctx, peer)
}

func (m *Manager) handlePeer(ctx context.Context, peer *Peer) {
	defer func() {
		peer.Close()
		m.mu.Lock()
		delete(m.peers, peer.Addr())
		// Recompute bestPeerHeight from remaining peers.
		var best uint32
		for _, p := range m.peers {
			if v := p.Version(); v != nil && v.StartHeight > best {
				best = v.StartHeight
			}
		}
		m.bestPeerHeight = best
		m.mu.Unlock()
		logging.L.Debug("peer disconnected", "component", "p2p", "addr", peer.Addr())
		metrics.Global.PeersDisconnects.Add(1)
		metrics.Global.PeersConnected.Add(-1)
	}()

	// Perform handshake.
	if err := m.handshake(peer); err != nil {
		logging.L.Warn("handshake failed", "component", "p2p", "addr", peer.Addr(), "error", err)
		return
	}

	m.mu.Lock()
	m.peers[peer.Addr()] = peer
	if peer.Version().StartHeight > m.bestPeerHeight {
		m.bestPeerHeight = peer.Version().StartHeight
	}
	m.mu.Unlock()
	m.peerStore.PutPeer(peer.Addr())

	logging.L.Info("peer connected", "component", "p2p", "addr", peer.Addr(), "version", peer.Version().Version, "height", peer.Version().StartHeight)
	metrics.Global.PeersConnected.Add(1)

	// Start write loop.
	go peer.WriteLoop()

	// Trigger initial sync if peer has more blocks.
	_, ourHeight := m.chain.Tip()
	if peer.Version().StartHeight > ourHeight {
		m.requestBlocks(peer)
	}

	// Message read loop.
	for {
		select {
		case <-ctx.Done():
			return
		case <-peer.Done():
			return
		default:
		}

		hdr, payload, err := peer.ReadMessage()
		if err != nil {
			logging.L.Debug("read error", "component", "p2p", "addr", peer.Addr(), "error", err)
			return
		}

		m.handleMessage(ctx, peer, hdr, payload)
	}
}

func (m *Manager) handshake(peer *Peer) error {
	if peer.IsInbound() {
		return m.handshakeInbound(peer)
	}
	return m.handshakeOutbound(peer)
}

// handshakeOutbound: we initiated the connection.
// Sequence: send version -> read version -> send verack -> read verack
func (m *Manager) handshakeOutbound(peer *Peer) error {
	peer.conn.SetDeadline(time.Now().Add(30 * time.Second))
	defer peer.conn.SetDeadline(time.Time{})

	if err := m.sendVersion(peer); err != nil {
		return fmt.Errorf("send version: %w", err)
	}

	if err := m.readAndProcessVersion(peer); err != nil {
		return err
	}

	// Read their verack (they send it after receiving our version).
	hdr, _, err := peer.ReadMessage()
	if err != nil {
		return fmt.Errorf("read verack: %w", err)
	}
	if hdr.CommandString() != protocol.CmdVerack {
		return fmt.Errorf("expected verack, got %s", hdr.CommandString())
	}

	// Send our verack.
	if err := m.sendVerack(peer); err != nil {
		return fmt.Errorf("send verack: %w", err)
	}

	return nil
}

// handshakeInbound: they connected to us.
// Sequence: read version -> send version -> send verack -> read verack
func (m *Manager) handshakeInbound(peer *Peer) error {
	peer.conn.SetDeadline(time.Now().Add(30 * time.Second))
	defer peer.conn.SetDeadline(time.Time{})

	if err := m.readAndProcessVersion(peer); err != nil {
		return err
	}

	if err := m.sendVersion(peer); err != nil {
		return fmt.Errorf("send version: %w", err)
	}

	// Send verack (we accepted their version).
	if err := m.sendVerack(peer); err != nil {
		return fmt.Errorf("send verack: %w", err)
	}

	// Read their verack.
	hdr, _, err := peer.ReadMessage()
	if err != nil {
		return fmt.Errorf("read verack: %w", err)
	}
	if hdr.CommandString() != protocol.CmdVerack {
		return fmt.Errorf("expected verack, got %s", hdr.CommandString())
	}

	return nil
}

func (m *Manager) sendVersion(peer *Peer) error {
	_, height := m.chain.Tip()
	msg := protocol.VersionMsg{
		Version:     protocol.ProtocolVersion,
		Services:    1,
		Timestamp:   time.Now().Unix(),
		AddrRecv:    peer.Addr(),
		AddrFrom:    m.listenAddr,
		Nonce:       m.localNonce,
		UserAgent:   version.UserAgent(),
		StartHeight: height,
	}

	var buf bytes.Buffer
	msg.Encode(&buf)
	payload := buf.Bytes()

	if err := protocol.EncodeMessageHeader(peer.conn, m.params.NetworkMagic, protocol.CmdVersion, payload); err != nil {
		return err
	}
	_, err := peer.conn.Write(payload)
	return err
}

func (m *Manager) sendVerack(peer *Peer) error {
	return protocol.EncodeMessageHeader(peer.conn, m.params.NetworkMagic, protocol.CmdVerack, nil)
}

func (m *Manager) readAndProcessVersion(peer *Peer) error {
	hdr, versionPayload, err := peer.ReadMessage()
	if err != nil {
		return fmt.Errorf("read version: %w", err)
	}
	if hdr.CommandString() != protocol.CmdVersion {
		return fmt.Errorf("expected version, got %s", hdr.CommandString())
	}

	var theirVersion protocol.VersionMsg
	if err := theirVersion.Decode(bytes.NewReader(versionPayload)); err != nil {
		return fmt.Errorf("decode version: %w", err)
	}

	if theirVersion.Nonce == m.localNonce {
		return fmt.Errorf("self-connection detected")
	}

	peer.SetVersion(&theirVersion)

	if m.timeSampler != nil && theirVersion.Timestamp != 0 {
		m.timeSampler.AddSample(peer.Addr(), theirVersion.Timestamp)
	}

	return nil
}

func (m *Manager) handleMessage(ctx context.Context, peer *Peer, hdr *protocol.MessageHeader, payload []byte) {
	cmd := hdr.CommandString()
	r := bytes.NewReader(payload)

	switch cmd {
	case protocol.CmdPing:
		var ping protocol.PingMsg
		if err := ping.Decode(r); err != nil {
			return
		}
		pong := protocol.PongMsg{Nonce: ping.Nonce}
		var buf bytes.Buffer
		pong.Encode(&buf)
		peer.SendMessage(protocol.CmdPong, buf.Bytes())

	case protocol.CmdPong:
		// Liveness confirmed; could update peer scoring.

	case protocol.CmdInv:
		var inv protocol.InvMsg
		if err := inv.Decode(r); err != nil {
			return
		}
		m.handleInv(peer, &inv)

	case protocol.CmdGetData:
		var getData protocol.GetDataMsg
		if err := getData.Decode(r); err != nil {
			return
		}
		m.handleGetData(peer, &getData)

	case protocol.CmdBlock:
		var block types.Block
		if err := block.Deserialize(r); err != nil {
			logging.L.Warn("bad block payload", "component", "p2p", "addr", peer.Addr(), "error", err)
			return
		}
		m.handleBlock(peer, &block)

	case protocol.CmdTx:
		var tx types.Transaction
		if err := tx.Deserialize(r); err != nil {
			logging.L.Warn("bad tx payload", "component", "p2p", "addr", peer.Addr(), "error", err)
			return
		}
		m.handleTx(peer, &tx)

	case protocol.CmdGetBlocks:
		var getBlocks protocol.GetBlocksMsg
		if err := getBlocks.Decode(r); err != nil {
			return
		}
		m.handleGetBlocks(peer, &getBlocks)

	case protocol.CmdAddr:
		var addr protocol.AddrMsg
		if err := addr.Decode(r); err != nil {
			return
		}
		for _, a := range addr.Addresses {
			m.peerStore.PutPeer(a)
		}

	default:
		logging.L.Debug("unknown command", "component", "p2p", "cmd", cmd, "addr", peer.Addr())
	}
}

func (m *Manager) handleInv(peer *Peer, inv *protocol.InvMsg) {
	var needed []protocol.InvVector
	blockCount := 0
	for _, iv := range inv.Inventory {
		peer.AddKnownInventory(iv.Hash)
		switch iv.Type {
		case protocol.InvTypeBlock:
			blockCount++
			// Only skip blocks that are on the active chain (in heightByHash).
			// Blocks that are only in the orphan pool or only in the disk index
			// (e.g. stored as side-chain blocks after a race) must be re-requested
			// so the node can process them once the parent becomes available.
			if !m.chain.HasBlockOnChain(iv.Hash) {
				needed = append(needed, iv)
			}
		case protocol.InvTypeTx:
			if !m.mempool.HasTx(iv.Hash) {
				needed = append(needed, iv)
			}
		}
	}

	if len(needed) > 0 {
		getData := protocol.GetDataMsg{Inventory: needed}
		var buf bytes.Buffer
		getData.Encode(&buf)
		peer.SendMessage(protocol.CmdGetData, buf.Bytes())
	}
}

func (m *Manager) handleGetData(peer *Peer, getData *protocol.GetDataMsg) {
	for _, iv := range getData.Inventory {
		switch iv.Type {
		case protocol.InvTypeBlock:
			block, err := m.chain.GetBlock(iv.Hash)
			if err != nil {
				continue
			}
			blockBytes, err := block.SerializeToBytes()
			if err != nil {
				continue
			}
			peer.SendMessage(protocol.CmdBlock, blockBytes)

		case protocol.InvTypeTx:
			tx, ok := m.mempool.GetTx(iv.Hash)
			if !ok {
				continue
			}
			txBytes, err := tx.SerializeToBytes()
			if err != nil {
				continue
			}
			peer.SendMessage(protocol.CmdTx, txBytes)
		}
	}
}

func (m *Manager) handleBlock(peer *Peer, block *types.Block) {
	blockHash := crypto.HashBlockHeader(&block.Header)
	peer.AddKnownInventory(blockHash)

	if m.seenBlocks.AddOrHas(blockHash) {
		return
	}

	height, err := m.chain.ProcessBlock(block)
	if err != nil {
		if errors.Is(err, chain.ErrSideChain) {
			logging.L.Debug("block stored as side chain", "component", "p2p", "hash", blockHash.ReverseString(), "height", height)
			return
		}
		// Block was not accepted onto the active chain. Remove from
		// seenBlocks so it can be re-delivered later. This covers:
		//  - Orphans: parent unknown due to reorg race; parent may
		//    arrive later and processOrphans will handle it, but if
		//    the orphan's parent was reorged away and back, the block
		//    needs to be re-deliverable.
		//  - Validation failures: transient state may resolve.
		// The orphan pool's own duplicate check prevents busy-loops.
		m.seenBlocks.Remove(blockHash)
		if !errors.Is(err, chain.ErrOrphanBlock) {
			logging.L.Warn("block rejected", "component", "p2p", "hash", blockHash.ReverseString(), "addr", peer.Addr(), "error", err)
		}
		return
	}

	// Update mempool: remove confirmed transactions and update tip height.
	var confirmedHashes []types.Hash
	for _, tx := range block.Transactions {
		txHash, hashErr := crypto.HashTransaction(&tx)
		if hashErr == nil {
			confirmedHashes = append(confirmedHashes, txHash)
		}
	}
	m.mempool.RemoveTxs(confirmedHashes)
	m.mempool.SetTipHeight(height)

	// Update best peer height tracking for IBD detection.
	peer.SetStartHeightIfGreater(height)
	m.mu.Lock()
	if height > m.bestPeerHeight {
		m.bestPeerHeight = height
	}
	m.mu.Unlock()

	logging.L.Info("block accepted from peer", "component", "p2p", "hash", blockHash.ReverseString(), "height", height, "addr", peer.Addr())
	m.BroadcastBlock(blockHash, block)
}

func (m *Manager) handleTx(peer *Peer, tx *types.Transaction) {
	txHash, err := crypto.HashTransaction(tx)
	if err != nil {
		return
	}
	peer.AddKnownInventory(txHash)

	if m.seenTxs.AddOrHas(txHash) {
		return
	}

	if _, err := m.mempool.AddTx(tx); err != nil {
		return
	}

	m.BroadcastTx(txHash)
}

func (m *Manager) handleGetBlocks(peer *Peer, msg *protocol.GetBlocksMsg) {
	_, tipHeight := m.chain.Tip()

	// Find the highest block in the locator that we know.
	startHeight := uint32(0)
	for _, hash := range msg.BlockLocatorHashes {
		if m.chain.HasBlock(hash) {
			header, err := m.chain.GetBlock(hash)
			if err == nil {
				h := crypto.HashBlockHeader(&header.Header)
				_ = h
				// Find the height of this block.
				for height := uint32(0); height <= tipHeight; height++ {
					bh, err := m.chain.GetHeaderByHeight(height)
					if err != nil {
						continue
					}
					if crypto.HashBlockHeader(bh) == hash {
						startHeight = height
						break
					}
				}
			}
			break
		}
	}

	// Send inv for blocks after the start.
	var invItems []protocol.InvVector
	for h := startHeight + 1; h <= tipHeight && len(invItems) < 500; h++ {
		header, err := m.chain.GetHeaderByHeight(h)
		if err != nil {
			break
		}
		hash := crypto.HashBlockHeader(header)
		invItems = append(invItems, protocol.InvVector{Type: protocol.InvTypeBlock, Hash: hash})
		if hash == msg.HashStop {
			break
		}
	}

	if len(invItems) > 0 {
		inv := protocol.InvMsg{Inventory: invItems}
		var buf bytes.Buffer
		inv.Encode(&buf)
		peer.SendMessage(protocol.CmdInv, buf.Bytes())
	}
}

// syncLoop periodically requests more blocks from the tallest peer.
// The getblocks handler returns up to 500 blocks at a time, so for long chains
// we need multiple rounds. This loop re-requests every second until caught up.
func (m *Manager) syncLoop(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.mu.RLock()
			var syncPeer *Peer
			var bestHeight uint32
			for _, p := range m.peers {
				v := p.Version()
				if v != nil && v.StartHeight > bestHeight {
					bestHeight = v.StartHeight
					syncPeer = p
				}
			}
			m.mu.RUnlock()
			if syncPeer != nil {
				m.requestBlocks(syncPeer)
			}
		}
	}
}

func (m *Manager) requestBlocks(peer *Peer) {
	tipHash, _ := m.chain.Tip()
	msg := protocol.GetBlocksMsg{
		Version:            protocol.ProtocolVersion,
		BlockLocatorHashes: []types.Hash{tipHash},
		HashStop:           types.ZeroHash,
	}
	var buf bytes.Buffer
	msg.Encode(&buf)
	peer.SendMessage(protocol.CmdGetBlocks, buf.Bytes())
}
