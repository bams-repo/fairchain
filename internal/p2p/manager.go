package p2p

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
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
	connectOnly []string // -connect peers: when non-empty, ONLY connect to these (no discovery)
	noSeedNodes bool     // -noseednode: suppress hardcoded SeedNodes from params
	timeSampler TimeSampler

	mu             sync.RWMutex
	peers          map[string]*Peer
	localNonce     uint64
	bestPeerHeight uint32

	// Ban list: IP → expiry time. Keyed by IP (no port) to match Bitcoin Core.
	banMu  sync.RWMutex
	banned map[string]time.Time

	// Reconnection backoff: addr → next allowed attempt time.
	backoffMu sync.Mutex
	backoff   map[string]time.Time
	backoffN  map[string]int // consecutive failure count per addr

	seenBlocks *boundedHashSet
	seenTxs    *boundedHashSet

	// Sync request throttle: prevents spamming getblocks when already waiting
	// for a response. Matches Bitcoin Core's single-flight sync approach.
	lastSyncReq   time.Time
	lastSyncReqMu sync.Mutex

	ctx      context.Context
	listener net.Listener
}

const (
	maxSeenBlocks = 10000
	maxSeenTxs    = 50000

	// Reconnection backoff parameters.
	backoffBase = 5 * time.Second
	backoffMax  = 10 * time.Minute

	// Peer store pruning: remove addresses not seen in this duration.
	peerStorePruneAge = 7 * 24 * time.Hour
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

// ManagerOptions holds optional configuration for the P2P manager.
type ManagerOptions struct {
	ConnectOnly []string // When non-empty, connect ONLY to these peers (Bitcoin Core -connect).
	NoSeedNodes bool     // Suppress hardcoded SeedNodes from ChainParams (Bitcoin Core -noseednode).
}

// NewManager creates a new P2P manager. ts may be nil if no time adjustment is needed.
func NewManager(p *params.ChainParams, c *chain.Chain, mp *mempool.Mempool, ps store.PeerStore, listenAddr string, maxIn, maxOut int, seeds []string, ts TimeSampler, opts *ManagerOptions) *Manager {
	var nonce uint64
	b := make([]byte, 8)
	rand.Read(b)
	nonce = binary.LittleEndian.Uint64(b)

	mgr := &Manager{
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
		banned:      make(map[string]time.Time),
		backoff:     make(map[string]time.Time),
		backoffN:    make(map[string]int),
		seenBlocks:  newBoundedHashSet(maxSeenBlocks),
		seenTxs:     newBoundedHashSet(maxSeenTxs),
	}

	if opts != nil {
		mgr.connectOnly = opts.ConnectOnly
		mgr.noSeedNodes = opts.NoSeedNodes
	}

	return mgr
}

// Start begins listening for connections and connecting to seed peers.
func (m *Manager) Start(ctx context.Context) error {
	m.ctx = ctx
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
	go m.pingLoop(ctx)
	go m.livenessLoop(ctx)
	go m.addrBroadcastLoop(ctx)
	if logging.DebugMode {
		go m.topologyLoop(ctx)
	}

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

// PeerInfos returns detailed info for all connected peers (for RPC).
func (m *Manager) PeerInfos() []PeerInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	infos := make([]PeerInfo, 0, len(m.peers))
	for _, p := range m.peers {
		infos = append(infos, p.Info())
	}
	return infos
}

// ConnectionCounts returns the number of inbound and outbound connections.
func (m *Manager) ConnectionCounts() (inbound, outbound int) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, p := range m.peers {
		if p.IsInbound() {
			inbound++
		} else {
			outbound++
		}
	}
	return
}

// AddNode attempts to connect to the given address. Returns an error if
// already connected or banned.
func (m *Manager) AddNode(addr string) error {
	if m.IsBanned(addr) {
		return fmt.Errorf("node %s is banned", addr)
	}
	m.mu.RLock()
	_, exists := m.peers[addr]
	m.mu.RUnlock()
	if exists {
		return fmt.Errorf("already connected to %s", addr)
	}
	go m.connectPeer(m.ctx, addr)
	return nil
}

// DisconnectNode disconnects a peer by address. Returns an error if not found.
func (m *Manager) DisconnectNode(addr string) error {
	m.mu.RLock()
	var target *Peer
	for a, p := range m.peers {
		if a == addr {
			target = p
			break
		}
	}
	m.mu.RUnlock()
	if target == nil {
		return fmt.Errorf("peer %s not found", addr)
	}
	target.Close()
	return nil
}

// BroadcastBlock announces a new block to all peers that don't already know it.
func (m *Manager) BroadcastBlock(hash types.Hash, block *types.Block) {
	m.seenBlocks.Add(hash)

	blockBytes, err := block.SerializeToBytes()
	if err != nil {
		logging.L.Error("failed to serialize block for relay", "component", "p2p", "hash", hash.ReverseString(), "error", err)
		return
	}

	inv := protocol.InvMsg{
		Inventory: []protocol.InvVector{
			{Type: protocol.InvTypeBlock, Hash: hash},
		},
	}
	var invBuf bytes.Buffer
	inv.Encode(&invBuf)
	invPayload := invBuf.Bytes()

	// Bitcoin Core (BIP 152) pushes full blocks to ~3 high-bandwidth relay
	// peers and sends inv to the rest. This prevents send-queue flooding on
	// nodes with many connections while keeping relay latency low.
	const maxDirectPush = 3

	m.mu.RLock()
	eligible := make([]*Peer, 0, len(m.peers))
	for _, p := range m.peers {
		if !p.HasKnownInventory(hash) {
			eligible = append(eligible, p)
		}
	}
	m.mu.RUnlock()

	shufflePeers(eligible)

	if logging.DebugMode {
		directAddrs := make([]string, 0, maxDirectPush)
		invAddrs := make([]string, 0, len(eligible))
		for i, p := range eligible {
			if i < maxDirectPush {
				directAddrs = append(directAddrs, p.Addr())
			} else {
				invAddrs = append(invAddrs, p.Addr())
			}
		}
		skipped := len(m.peers) - len(eligible)
		logging.L.Debug("[dbg] BroadcastBlock",
			"hash", hash.ReverseString()[:16],
			"total_peers", len(m.peers),
			"eligible", len(eligible),
			"skipped_known", skipped,
			"direct_push", directAddrs,
			"inv_to", invAddrs)
	}

	for i, p := range eligible {
		p.AddKnownInventory(hash)
		if i < maxDirectPush {
			p.SendMessage(protocol.CmdBlock, blockBytes)
		} else {
			p.SendMessage(protocol.CmdInv, invPayload)
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

// --- Ban management ---

// extractIP strips the port from an addr string to get the bare IP,
// matching Bitcoin Core's per-IP (not per-connection) ban behavior.
func extractIP(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}

// BanPeer bans a peer's IP for BanDuration.
func (m *Manager) BanPeer(addr string) {
	ip := extractIP(addr)
	m.banMu.Lock()
	m.banned[ip] = time.Now().Add(BanDuration)
	m.banMu.Unlock()
	logging.L.Warn("peer banned", "component", "p2p", "ip", ip, "duration", BanDuration)
}

// IsBanned checks if an IP is currently banned.
func (m *Manager) IsBanned(addr string) bool {
	ip := extractIP(addr)
	m.banMu.RLock()
	expiry, ok := m.banned[ip]
	m.banMu.RUnlock()
	if !ok {
		return false
	}
	if time.Now().After(expiry) {
		m.banMu.Lock()
		delete(m.banned, ip)
		m.banMu.Unlock()
		return false
	}
	return true
}

// addMisbehavior increases a peer's ban score and disconnects+bans if threshold reached.
// In -connect mode the operator explicitly chose these peers, so banning them
// would sever the only links the node has. Log the misbehavior but keep the connection.
func (m *Manager) addMisbehavior(peer *Peer, score int32, reason string) {
	if len(m.connectOnly) > 0 {
		logging.L.Debug("peer misbehavior (connect-only, ban suppressed)", "component", "p2p",
			"addr", peer.Addr(), "delta", score, "reason", reason)
		return
	}
	newScore := peer.AddBanScore(score)
	if newScore >= BanThreshold {
		logging.L.Warn("peer reached ban threshold", "component", "p2p",
			"addr", peer.Addr(), "score", newScore, "reason", reason)
		m.BanPeer(peer.Addr())
		peer.Close()
	} else {
		logging.L.Debug("peer misbehavior", "component", "p2p",
			"addr", peer.Addr(), "score", newScore, "delta", score, "reason", reason)
	}
}

// --- Reconnection backoff ---

func (m *Manager) canReconnect(addr string) bool {
	m.backoffMu.Lock()
	defer m.backoffMu.Unlock()
	next, ok := m.backoff[addr]
	if !ok {
		return true
	}
	return time.Now().After(next)
}

func (m *Manager) recordConnectFailure(addr string) {
	m.backoffMu.Lock()
	defer m.backoffMu.Unlock()
	m.backoffN[addr]++
	n := m.backoffN[addr]
	delay := backoffBase * time.Duration(1<<min(n, 10))
	if delay > backoffMax {
		delay = backoffMax
	}
	m.backoff[addr] = time.Now().Add(delay)
}

func (m *Manager) recordConnectSuccess(addr string) {
	m.backoffMu.Lock()
	defer m.backoffMu.Unlock()
	delete(m.backoff, addr)
	delete(m.backoffN, addr)
}

// --- Outbound counting ---

func (m *Manager) outboundCount() int {
	count := 0
	for _, p := range m.peers {
		if !p.IsInbound() {
			count++
		}
	}
	return count
}

// --- Inbound eviction (Bitcoin Core parity) ---
// When inbound slots are full and a new inbound peer connects, evict the
// worst-performing inbound peer. "Worst" = highest ping latency among those
// with the shortest connection time, excluding peers that recently relayed
// useful blocks/txs. Simplified version: evict the inbound peer with the
// highest ping latency.

func (m *Manager) maybeEvictInbound() {
	m.mu.RLock()
	var inbound []*Peer
	for _, p := range m.peers {
		if p.IsInbound() {
			inbound = append(inbound, p)
		}
	}
	m.mu.RUnlock()

	if len(inbound) < m.maxInbound {
		return
	}

	// Sort by ping latency descending — evict the slowest.
	sort.Slice(inbound, func(i, j int) bool {
		return inbound[i].PingLatency() > inbound[j].PingLatency()
	})

	victim := inbound[0]
	logging.L.Info("evicting inbound peer for slot", "component", "p2p",
		"addr", victim.Addr(), "ping_ms", victim.PingLatency().Milliseconds())
	victim.Close()
}

// --- Connection loops ---

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

		if m.IsBanned(conn.RemoteAddr().String()) {
			conn.Close()
			continue
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
			m.maybeEvictInbound()
		}

		peer := NewPeer(conn, true, m.params.NetworkMagic)
		go m.handlePeer(ctx, peer)
	}
}

func (m *Manager) connectSeeds(ctx context.Context) {
	if len(m.connectOnly) > 0 {
		if logging.DebugMode {
			logging.L.Debug("[dbg] connectSeeds: connect-only mode", "targets", m.connectOnly)
		}
		for _, addr := range m.connectOnly {
			select {
			case <-ctx.Done():
				return
			default:
			}
			m.connectPeer(ctx, addr)
		}
		return
	}

	allSeeds := make([]string, 0, len(m.seedPeers)+len(m.params.SeedNodes))
	allSeeds = append(allSeeds, m.seedPeers...)
	if !m.noSeedNodes {
		allSeeds = append(allSeeds, m.params.SeedNodes...)
	}

	if stored, err := m.peerStore.GetPeers(); err == nil {
		allSeeds = append(allSeeds, stored...)
	}

	if logging.DebugMode {
		logging.L.Debug("[dbg] connectSeeds",
			"cli_seeds", len(m.seedPeers),
			"param_seeds", len(m.params.SeedNodes),
			"total_targets", len(allSeeds))
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
			outCount := m.outboundCount()
			inCount := len(m.peers) - outCount
			connected := make(map[string]bool, len(m.peers)*2)
			for addr, p := range m.peers {
				connected[addr] = true
				if v := p.Version(); v != nil && v.AddrFrom != "" {
					connected[v.AddrFrom] = true
				}
			}
			m.mu.RUnlock()

			if logging.DebugMode {
				logging.L.Debug("[dbg] reconnectLoop tick",
					"outbound", outCount,
					"inbound", inCount,
					"max_outbound", m.maxOutbound,
					"known_addrs", len(connected))
			}

			if outCount >= m.maxOutbound {
				continue
			}

			// -connect mode: always maintain connections to the explicit list,
			// then also connect to discovered peers for mesh formation.
			// Bitcoin Core's strict -connect prevents discovery, but for a small
			// network this creates an unworkable hub-and-spoke topology.
			if len(m.connectOnly) > 0 {
				for _, addr := range m.connectOnly {
					if !connected[addr] && !m.IsBanned(addr) && m.canReconnect(addr) {
						m.connectPeer(ctx, addr)
						connected[addr] = true
					}
				}
				// Also connect to discovered peers for mesh formation.
				if stored, err := m.peerStore.GetPeers(); err == nil {
					for _, addr := range stored {
						if outCount >= m.maxOutbound {
							break
						}
						if !connected[addr] && !m.IsBanned(addr) && m.canReconnect(addr) && addr != m.listenAddr {
							m.connectPeer(ctx, addr)
							connected[addr] = true
							outCount++
						}
					}
				}
				continue
			}

			allSeeds := make([]string, 0, len(m.seedPeers)+len(m.params.SeedNodes))
			allSeeds = append(allSeeds, m.seedPeers...)
			if !m.noSeedNodes {
				allSeeds = append(allSeeds, m.params.SeedNodes...)
			}
			for _, addr := range allSeeds {
				if !connected[addr] && !m.IsBanned(addr) && m.canReconnect(addr) {
					m.connectPeer(ctx, addr)
					connected[addr] = true
				}
			}

			if stored, err := m.peerStore.GetPeers(); err == nil {
				for _, addr := range stored {
					if !connected[addr] && !m.IsBanned(addr) && m.canReconnect(addr) {
						m.connectPeer(ctx, addr)
						break
					}
				}
			}
		}
	}
}

func (m *Manager) connectPeer(ctx context.Context, addr string) {
	if addr == m.listenAddr {
		return
	}

	m.mu.RLock()
	if _, exists := m.peers[addr]; exists {
		m.mu.RUnlock()
		if logging.DebugMode {
			logging.L.Debug("[dbg] connectPeer: already connected", "addr", addr)
		}
		return
	}
	for _, p := range m.peers {
		if v := p.Version(); v != nil && v.AddrFrom == addr {
			m.mu.RUnlock()
			if logging.DebugMode {
				logging.L.Debug("[dbg] connectPeer: duplicate via listen addr", "addr", addr, "existing", p.Addr())
			}
			return
		}
	}
	m.mu.RUnlock()

	if m.IsBanned(addr) {
		return
	}

	if logging.DebugMode {
		logging.L.Debug("[dbg] connectPeer: dialing", "addr", addr)
	}

	conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		if logging.DebugMode {
			logging.L.Debug("[dbg] connectPeer: dial failed", "addr", addr, "error", err)
		}
		m.recordConnectFailure(addr)
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
		var best uint32
		for _, p := range m.peers {
			if v := p.Version(); v != nil && v.StartHeight > best {
				best = v.StartHeight
			}
		}
		m.bestPeerHeight = best
		m.mu.Unlock()
		if logging.DebugMode {
			m.mu.RLock()
			remaining := len(m.peers)
			m.mu.RUnlock()
			logging.L.Debug("[dbg] peer disconnected",
				"addr", peer.Addr(),
				"inbound", peer.IsInbound(),
				"remaining_peers", remaining)
		} else {
			logging.L.Debug("peer disconnected", "component", "p2p", "addr", peer.Addr())
		}
		metrics.Global.PeersDisconnects.Add(1)
		metrics.Global.PeersConnected.Add(-1)
	}()

	if err := m.handshake(peer); err != nil {
		logging.L.Warn("handshake failed", "component", "p2p", "addr", peer.Addr(), "error", err)
		if !peer.IsInbound() {
			m.recordConnectFailure(peer.Addr())
		}
		return
	}

	m.recordConnectSuccess(peer.Addr())

	m.mu.Lock()
	m.peers[peer.Addr()] = peer
	if peer.Version().StartHeight > m.bestPeerHeight {
		m.bestPeerHeight = peer.Version().StartHeight
	}
	m.mu.Unlock()

	// Store the peer's advertised listen address (AddrFrom) rather than the
	// ephemeral TCP address. This ensures addr gossip propagates reachable
	// addresses, matching Bitcoin Core's behavior of tracking listen addrs.
	listenAddr := peer.Version().AddrFrom
	if listenAddr != "" && listenAddr != m.listenAddr {
		m.peerStore.PutPeer(listenAddr)
	} else if peer.Addr() != m.listenAddr {
		m.peerStore.PutPeer(peer.Addr())
	}

	if logging.DebugMode {
		m.mu.RLock()
		totalPeers := len(m.peers)
		m.mu.RUnlock()
		logging.L.Info("[dbg] peer connected",
			"addr", peer.Addr(),
			"listen_addr", listenAddr,
			"inbound", peer.IsInbound(),
			"version", peer.Version().Version,
			"start_height", peer.Version().StartHeight,
			"user_agent", peer.Version().UserAgent,
			"total_peers", totalPeers)
	} else {
		logging.L.Info("peer connected", "component", "p2p", "addr", peer.Addr(), "version", peer.Version().Version, "height", peer.Version().StartHeight)
	}
	metrics.Global.PeersConnected.Add(1)

	go peer.WriteLoop()

	m.sendGetAddr(peer)

	_, ourHeight := m.chain.Tip()
	if peer.Version().StartHeight > ourHeight {
		m.requestBlocks(peer)
	}

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

		if !peer.CheckRateLimit() {
			m.addMisbehavior(peer, 10, "message rate limit exceeded")
			if peer.BanScore() >= BanThreshold {
				return
			}
		}

		m.handleMessage(ctx, peer, hdr, payload)
	}
}

// --- Ping/pong keepalive (Bitcoin Core parity: BIP 31) ---

// pingLoop sends a ping to every connected peer every PingInterval.
func (m *Manager) pingLoop(ctx context.Context) {
	ticker := time.NewTicker(PingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.mu.RLock()
			peers := make([]*Peer, 0, len(m.peers))
			for _, p := range m.peers {
				peers = append(peers, p)
			}
			m.mu.RUnlock()

			for _, p := range peers {
				nonce := m.randomNonce()
				p.SetPingNonce(nonce)
				ping := protocol.PingMsg{Nonce: nonce}
				var buf bytes.Buffer
				ping.Encode(&buf)
				p.SendMessage(protocol.CmdPing, buf.Bytes())
			}
		}
	}
}

// livenessLoop checks for peers that have not responded to pings within
// the PongTimeout and disconnects them. Runs every 30 seconds.
func (m *Manager) livenessLoop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.mu.RLock()
			peers := make([]*Peer, 0, len(m.peers))
			for _, p := range m.peers {
				peers = append(peers, p)
			}
			m.mu.RUnlock()

			for _, p := range peers {
				if p.PongOverdue() {
					logging.L.Warn("peer pong timeout", "component", "p2p",
						"addr", p.Addr(), "timeout", PongTimeout)
					p.Close()
				}
			}

			// Prune expired bans.
			m.banMu.Lock()
			now := time.Now()
			for ip, expiry := range m.banned {
				if now.After(expiry) {
					delete(m.banned, ip)
				}
			}
			m.banMu.Unlock()
		}
	}
}

func (m *Manager) randomNonce() uint64 {
	b := make([]byte, 8)
	rand.Read(b)
	return binary.LittleEndian.Uint64(b)
}

// --- Handshake ---

func (m *Manager) handshake(peer *Peer) error {
	if peer.IsInbound() {
		return m.handshakeInbound(peer)
	}
	return m.handshakeOutbound(peer)
}

func (m *Manager) handshakeOutbound(peer *Peer) error {
	peer.conn.SetDeadline(time.Now().Add(30 * time.Second))
	defer peer.conn.SetDeadline(time.Time{})

	if err := m.sendVersion(peer); err != nil {
		return fmt.Errorf("send version: %w", err)
	}

	if err := m.readAndProcessVersion(peer); err != nil {
		return err
	}

	hdr, _, err := peer.ReadMessage()
	if err != nil {
		return fmt.Errorf("read verack: %w", err)
	}
	if hdr.CommandString() != protocol.CmdVerack {
		return fmt.Errorf("expected verack, got %s", hdr.CommandString())
	}

	if err := m.sendVerack(peer); err != nil {
		return fmt.Errorf("send verack: %w", err)
	}

	return nil
}

func (m *Manager) handshakeInbound(peer *Peer) error {
	peer.conn.SetDeadline(time.Now().Add(30 * time.Second))
	defer peer.conn.SetDeadline(time.Time{})

	if err := m.readAndProcessVersion(peer); err != nil {
		return err
	}

	if err := m.sendVersion(peer); err != nil {
		return fmt.Errorf("send version: %w", err)
	}

	if err := m.sendVerack(peer); err != nil {
		return fmt.Errorf("send verack: %w", err)
	}

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

// --- Message handling ---

func (m *Manager) handleMessage(ctx context.Context, peer *Peer, hdr *protocol.MessageHeader, payload []byte) {
	cmd := hdr.CommandString()
	r := bytes.NewReader(payload)

	if logging.DebugMode && cmd != protocol.CmdPing && cmd != protocol.CmdPong {
		logging.L.Debug("[dbg] msg recv",
			"cmd", cmd,
			"peer", peer.Addr(),
			"size", len(payload))
	}

	switch cmd {
	case protocol.CmdPing:
		var ping protocol.PingMsg
		if err := ping.Decode(r); err != nil {
			m.addMisbehavior(peer, 1, "malformed ping")
			return
		}
		pong := protocol.PongMsg{Nonce: ping.Nonce}
		var buf bytes.Buffer
		pong.Encode(&buf)
		peer.SendMessage(protocol.CmdPong, buf.Bytes())

	case protocol.CmdPong:
		var pong protocol.PongMsg
		if err := pong.Decode(r); err != nil {
			m.addMisbehavior(peer, 1, "malformed pong")
			return
		}
		if !peer.HandlePong(pong.Nonce) {
			m.addMisbehavior(peer, 1, "unexpected pong nonce")
		}

	case protocol.CmdInv:
		var inv protocol.InvMsg
		if err := inv.Decode(r); err != nil {
			m.addMisbehavior(peer, 20, "malformed inv")
			return
		}
		m.handleInv(peer, &inv)

	case protocol.CmdGetData:
		var getData protocol.GetDataMsg
		if err := getData.Decode(r); err != nil {
			m.addMisbehavior(peer, 20, "malformed getdata")
			return
		}
		m.handleGetData(peer, &getData)

	case protocol.CmdBlock:
		var block types.Block
		if err := block.Deserialize(r); err != nil {
			logging.L.Warn("bad block payload", "component", "p2p", "addr", peer.Addr(), "error", err)
			m.addMisbehavior(peer, 20, "malformed block")
			return
		}
		m.handleBlock(peer, &block)

	case protocol.CmdTx:
		var tx types.Transaction
		if err := tx.Deserialize(r); err != nil {
			logging.L.Warn("bad tx payload", "component", "p2p", "addr", peer.Addr(), "error", err)
			m.addMisbehavior(peer, 10, "malformed tx")
			return
		}
		m.handleTx(peer, &tx)

	case protocol.CmdGetBlocks:
		var getBlocks protocol.GetBlocksMsg
		if err := getBlocks.Decode(r); err != nil {
			m.addMisbehavior(peer, 10, "malformed getblocks")
			return
		}
		m.handleGetBlocks(peer, &getBlocks)

	case protocol.CmdAddr:
		var addr protocol.AddrMsg
		if err := addr.Decode(r); err != nil {
			m.addMisbehavior(peer, 10, "malformed addr")
			return
		}
		for _, a := range addr.Addresses {
			if !m.IsBanned(a) && a != m.listenAddr {
				m.peerStore.PutPeer(a)
			}
		}

	case protocol.CmdGetAddr:
		m.handleGetAddr(peer)

	default:
		logging.L.Debug("unknown command", "component", "p2p", "cmd", cmd, "addr", peer.Addr())
	}
}

func (m *Manager) handleInv(peer *Peer, inv *protocol.InvMsg) {
	var needed []protocol.InvVector
	var alreadyHaveBlocks, alreadyHaveTxs int
	for _, iv := range inv.Inventory {
		peer.AddKnownInventory(iv.Hash)
		switch iv.Type {
		case protocol.InvTypeBlock:
			if !m.chain.HasBlockOnChain(iv.Hash) {
				needed = append(needed, iv)
			} else {
				alreadyHaveBlocks++
			}
		case protocol.InvTypeTx:
			if !m.mempool.HasTx(iv.Hash) {
				needed = append(needed, iv)
			} else {
				alreadyHaveTxs++
			}
		}
	}

	if logging.DebugMode {
		var neededBlocks, neededTxs int
		for _, iv := range needed {
			if iv.Type == protocol.InvTypeBlock {
				neededBlocks++
			} else {
				neededTxs++
			}
		}
		logging.L.Debug("[dbg] handleInv",
			"peer", peer.Addr(),
			"total_items", len(inv.Inventory),
			"need_blocks", neededBlocks,
			"need_txs", neededTxs,
			"already_blocks", alreadyHaveBlocks,
			"already_txs", alreadyHaveTxs)
	}

	if len(needed) > 0 {
		getData := protocol.GetDataMsg{Inventory: needed}
		var buf bytes.Buffer
		getData.Encode(&buf)
		peer.SendMessage(protocol.CmdGetData, buf.Bytes())
	}
}

func (m *Manager) handleGetData(peer *Peer, getData *protocol.GetDataMsg) {
	var sentBlocks, sentTxs, missingBlocks, missingTxs int
	for _, iv := range getData.Inventory {
		switch iv.Type {
		case protocol.InvTypeBlock:
			block, err := m.chain.GetBlock(iv.Hash)
			if err != nil {
				missingBlocks++
				if logging.DebugMode {
					logging.L.Debug("[dbg] getdata: block not found",
						"peer", peer.Addr(),
						"hash", iv.Hash.ReverseString()[:16],
						"error", err)
				}
				continue
			}
			blockBytes, err := block.SerializeToBytes()
			if err != nil {
				continue
			}
			peer.SendMessage(protocol.CmdBlock, blockBytes)
			sentBlocks++

		case protocol.InvTypeTx:
			tx, ok := m.mempool.GetTx(iv.Hash)
			if !ok {
				missingTxs++
				continue
			}
			txBytes, err := tx.SerializeToBytes()
			if err != nil {
				continue
			}
			peer.SendMessage(protocol.CmdTx, txBytes)
			sentTxs++
		}
	}
	if logging.DebugMode {
		logging.L.Debug("[dbg] handleGetData",
			"peer", peer.Addr(),
			"requested", len(getData.Inventory),
			"sent_blocks", sentBlocks,
			"sent_txs", sentTxs,
			"missing_blocks", missingBlocks,
			"missing_txs", missingTxs)
	}
}

func (m *Manager) handleBlock(peer *Peer, block *types.Block) {
	blockHash := crypto.HashBlockHeader(&block.Header)
	peer.AddKnownInventory(blockHash)

	if m.seenBlocks.AddOrHas(blockHash) {
		if logging.DebugMode {
			logging.L.Debug("[dbg] handleBlock: already seen, skipping",
				"hash", blockHash.ReverseString()[:16],
				"peer", peer.Addr())
		}
		return
	}

	if logging.DebugMode {
		_, ourHeight := m.chain.Tip()
		logging.L.Debug("[dbg] handleBlock: processing",
			"hash", blockHash.ReverseString()[:16],
			"parent", block.Header.PrevBlock.ReverseString()[:16],
			"peer", peer.Addr(),
			"our_height", ourHeight)
	}

	height, err := m.chain.ProcessBlock(block)
	if err != nil {
		if errors.Is(err, chain.ErrSideChain) {
			logging.L.Info("block stored as side chain", "component", "p2p",
				"hash", blockHash.ReverseString(), "height", height, "addr", peer.Addr())
			if logging.DebugMode {
				_, tipH := m.chain.Tip()
				logging.L.Debug("[dbg] side chain detail",
					"hash", blockHash.ReverseString()[:16],
					"side_height", height,
					"main_tip", tipH)
			}
			return
		}
		m.seenBlocks.Remove(blockHash)
		if errors.Is(err, chain.ErrOrphanBlock) {
			logging.L.Info("orphan block, requesting chain from peer", "component", "p2p",
				"hash", blockHash.ReverseString(),
				"parent", block.Header.PrevBlock.ReverseString(),
				"addr", peer.Addr())
			m.requestBlocks(peer)
			return
		}
		errStr := err.Error()
		if strings.Contains(errStr, "already in chain") {
			if logging.DebugMode {
				logging.L.Debug("[dbg] handleBlock: already in chain",
					"hash", blockHash.ReverseString()[:16],
					"peer", peer.Addr())
			}
			return
		}
		logging.L.Warn("block rejected", "component", "p2p",
			"hash", blockHash.ReverseString(), "addr", peer.Addr(), "error", err)
		if strings.Contains(errStr, "proof of work") || strings.Contains(errStr, "merkle") || strings.Contains(errStr, "bits mismatch") {
			m.addMisbehavior(peer, 100, "invalid block: "+errStr)
		} else {
			m.addMisbehavior(peer, 10, "rejected block: "+errStr)
		}
		return
	}

	var confirmedHashes []types.Hash
	for _, tx := range block.Transactions {
		txHash, hashErr := crypto.HashTransaction(&tx)
		if hashErr == nil {
			confirmedHashes = append(confirmedHashes, txHash)
		}
	}
	m.mempool.RemoveTxs(confirmedHashes)
	m.mempool.SetTipHeight(height)

	peer.SetStartHeightIfGreater(height)
	m.mu.Lock()
	if height > m.bestPeerHeight {
		m.bestPeerHeight = height
	}
	m.mu.Unlock()

	logging.L.Info("block accepted from peer", "component", "p2p",
		"hash", blockHash.ReverseString(), "height", height, "addr", peer.Addr())
	m.BroadcastBlock(blockHash, block)

	if peer.BestHeight() > height {
		if logging.DebugMode {
			logging.L.Debug("[dbg] peer still ahead, requesting more",
				"peer", peer.Addr(),
				"peer_height", peer.BestHeight(),
				"our_height", height)
		}
		m.requestBlocks(peer)
	}
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

	startHeight := uint32(0)
	for _, hash := range msg.BlockLocatorHashes {
		if height, ok := m.chain.FindMainChainHash(hash); ok {
			startHeight = height
			break
		}
	}

	if logging.DebugMode {
		logging.L.Debug("[dbg] handleGetBlocks",
			"peer", peer.Addr(),
			"locator_len", len(msg.BlockLocatorHashes),
			"resolved_start", startHeight,
			"our_tip", tipHeight,
			"blocks_to_send", int(tipHeight)-int(startHeight))
	}

	sent := 0
	for h := startHeight + 1; h <= tipHeight && sent < 500; h++ {
		header, err := m.chain.GetHeaderByHeight(h)
		if err != nil {
			break
		}
		hash := crypto.HashBlockHeader(header)
		block, err := m.chain.GetBlock(hash)
		if err != nil {
			break
		}
		blockBytes, err := block.SerializeToBytes()
		if err != nil {
			break
		}
		peer.AddKnownInventory(hash)
		peer.SendMessage(protocol.CmdBlock, blockBytes)
		sent++
		if hash == msg.HashStop {
			break
		}
	}

	if logging.DebugMode && sent > 0 {
		logging.L.Debug("[dbg] handleGetBlocks sent",
			"peer", peer.Addr(),
			"blocks_sent", sent,
			"from_height", startHeight+1,
			"to_height", startHeight+uint32(sent))
	}
}

// --- Addr gossip (Bitcoin Core parity) ---

// handleGetAddr responds to a getaddr request with known peer addresses.
// Bitcoin Core only responds to one getaddr per connection to prevent
// topology scraping via repeated reconnection.
func (m *Manager) handleGetAddr(peer *Peer) {
	if !peer.MarkGetAddrResponded() {
		return
	}
	addrs := m.gatherAddresses(1000)
	if len(addrs) == 0 {
		return
	}
	msg := protocol.AddrMsg{Addresses: addrs}
	var buf bytes.Buffer
	msg.Encode(&buf)
	peer.SendMessage(protocol.CmdAddr, buf.Bytes())
}

// gatherAddresses collects up to limit known peer addresses for addr relay.
// Prefers advertised listen addresses (AddrFrom) over ephemeral connection
// addresses, matching Bitcoin Core's behavior of gossiping reachable addrs.
func (m *Manager) gatherAddresses(limit int) []string {
	m.mu.RLock()
	connected := make([]string, 0, len(m.peers))
	for _, p := range m.peers {
		if v := p.Version(); v != nil && v.AddrFrom != "" {
			connected = append(connected, v.AddrFrom)
		} else {
			connected = append(connected, p.Addr())
		}
	}
	m.mu.RUnlock()

	stored, _ := m.peerStore.GetPeers()

	seen := make(map[string]struct{})
	var addrs []string

	for _, addr := range connected {
		if addr == m.listenAddr {
			continue
		}
		if _, ok := seen[addr]; ok {
			continue
		}
		seen[addr] = struct{}{}
		addrs = append(addrs, addr)
		if len(addrs) >= limit {
			return addrs
		}
	}

	for _, addr := range stored {
		if addr == m.listenAddr {
			continue
		}
		if _, ok := seen[addr]; ok {
			continue
		}
		seen[addr] = struct{}{}
		addrs = append(addrs, addr)
		if len(addrs) >= limit {
			return addrs
		}
	}

	return addrs
}

// shufflePeers performs a Fisher-Yates shuffle on a peer slice using crypto/rand.
func shufflePeers(peers []*Peer) {
	for i := len(peers) - 1; i > 0; i-- {
		b := make([]byte, 8)
		rand.Read(b)
		j := int(binary.LittleEndian.Uint64(b) % uint64(i+1))
		peers[i], peers[j] = peers[j], peers[i]
	}
}

// sendGetAddr sends a getaddr request to a peer.
func (m *Manager) sendGetAddr(peer *Peer) {
	peer.SendMessage(protocol.CmdGetAddr, nil)
}

// addrBroadcastLoop periodically sends a small set of known peer addresses to
// a random subset of connected peers. Bitcoin Core uses Poisson-timed relay
// (~24h average for self-advertisement); we use 2 minutes and send to only
// 2 random peers per tick to avoid addr gossip flooding the message loop.
func (m *Manager) addrBroadcastLoop(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			addrs := m.gatherAddresses(10)
			if len(addrs) == 0 {
				continue
			}
			msg := protocol.AddrMsg{Addresses: addrs}
			var buf bytes.Buffer
			msg.Encode(&buf)
			payload := buf.Bytes()

			m.mu.RLock()
			peers := make([]*Peer, 0, len(m.peers))
			for _, p := range m.peers {
				peers = append(peers, p)
			}
			m.mu.RUnlock()

			shufflePeers(peers)
			sent := 0
			for _, p := range peers {
				if sent >= 2 {
					break
				}
				p.SendLowPriority(protocol.CmdAddr, payload)
				sent++
			}
		}
	}
}

// topologyLoop periodically dumps the full peer table when -debug is active.
// Logs every connected peer's address, direction, best height, queue usage,
// and version info. Runs every 10 seconds.
func (m *Manager) topologyLoop(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, ourHeight := m.chain.Tip()
			m.mu.RLock()
			peerCount := len(m.peers)
			if peerCount == 0 {
				m.mu.RUnlock()
				logging.L.Debug("[dbg] topology: no peers connected", "our_height", ourHeight)
				continue
			}
			type peerInfo struct {
				addr       string
				listenAddr string
				direction  string
				bestHeight uint32
				queueLen   int
				queueCap   int
				version    string
			}
			infos := make([]peerInfo, 0, peerCount)
			for _, p := range m.peers {
				dir := "out"
				if p.IsInbound() {
					dir = "in"
				}
				la := ""
				if v := p.Version(); v != nil {
					la = v.AddrFrom
				}
				infos = append(infos, peerInfo{
					addr:       p.Addr(),
					listenAddr: la,
					direction:  dir,
					bestHeight: p.BestHeight(),
					queueLen:   len(p.SendQueue()),
					queueCap:   cap(p.SendQueue()),
				})
			}
			m.mu.RUnlock()

			logging.L.Debug("[dbg] ═══ PEER TOPOLOGY ═══",
				"our_height", ourHeight,
				"total_peers", peerCount)
			for _, pi := range infos {
				logging.L.Debug("[dbg]   peer",
					"addr", pi.addr,
					"listen", pi.listenAddr,
					"dir", pi.direction,
					"best_h", pi.bestHeight,
					"queue", fmt.Sprintf("%d/%d", pi.queueLen, pi.queueCap))
			}
		}
	}
}

// --- Sync ---

func (m *Manager) syncLoop(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, ourHeight := m.chain.Tip()

			m.mu.RLock()
			var syncPeer *Peer
			var bestHeight uint32
			for _, p := range m.peers {
				ph := p.BestHeight()
				if ph > bestHeight {
					bestHeight = ph
					syncPeer = p
				}
			}
			m.mu.RUnlock()

			if syncPeer != nil && bestHeight > ourHeight {
				if logging.DebugMode {
					logging.L.Debug("[dbg] syncLoop: behind, requesting blocks",
						"our_height", ourHeight,
						"best_peer_height", bestHeight,
						"sync_peer", syncPeer.Addr())
				}
				m.requestBlocks(syncPeer)
			}
		}
	}
}

func (m *Manager) requestBlocks(peer *Peer) {
	m.lastSyncReqMu.Lock()
	if time.Since(m.lastSyncReq) < 500*time.Millisecond {
		m.lastSyncReqMu.Unlock()
		logging.L.Debug("requestBlocks throttled", "component", "p2p", "peer", peer.Addr())
		return
	}
	m.lastSyncReq = time.Now()
	m.lastSyncReqMu.Unlock()

	locator := m.chain.BlockLocator()
	if len(locator) == 0 {
		return
	}

	if logging.DebugMode {
		_, ourHeight := m.chain.Tip()
		locatorTip := "empty"
		if len(locator) > 0 {
			locatorTip = locator[0].ReverseString()[:16]
		}
		logging.L.Debug("[dbg] requestBlocks",
			"preferred_peer", peer.Addr(),
			"our_height", ourHeight,
			"locator_len", len(locator),
			"locator_tip", locatorTip)
	}

	msg := protocol.GetBlocksMsg{
		Version:            protocol.ProtocolVersion,
		BlockLocatorHashes: locator,
		HashStop:           types.ZeroHash,
	}
	var buf bytes.Buffer
	msg.Encode(&buf)
	payload := buf.Bytes()

	if peer.TrySendMessage(protocol.CmdGetBlocks, payload) {
		if logging.DebugMode {
			logging.L.Debug("[dbg] requestBlocks: sent to preferred peer", "peer", peer.Addr())
		}
		return
	}
	if logging.DebugMode {
		logging.L.Debug("[dbg] requestBlocks: preferred peer queue full, trying others",
			"peer", peer.Addr(),
			"queue_len", len(peer.SendQueue()))
	}
	m.mu.RLock()
	candidates := make([]*Peer, 0, len(m.peers))
	for _, p := range m.peers {
		if p != peer {
			candidates = append(candidates, p)
		}
	}
	m.mu.RUnlock()
	shufflePeers(candidates)
	for _, p := range candidates {
		if p.TrySendMessage(protocol.CmdGetBlocks, payload) {
			if logging.DebugMode {
				logging.L.Debug("[dbg] requestBlocks: sent to fallback peer", "peer", p.Addr())
			}
			return
		}
	}
	logging.L.Warn("all peer queues full, could not send getblocks", "component", "p2p")
}
