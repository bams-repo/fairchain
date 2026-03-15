package chain

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"sync"
	"time"

	"github.com/bams-repo/fairchain/internal/consensus"
	"github.com/bams-repo/fairchain/internal/crypto"
	"github.com/bams-repo/fairchain/internal/logging"
	"github.com/bams-repo/fairchain/internal/metrics"
	"github.com/bams-repo/fairchain/internal/params"
	"github.com/bams-repo/fairchain/internal/store"
	"github.com/bams-repo/fairchain/internal/types"
	"github.com/bams-repo/fairchain/internal/utxo"
)

const MaxOrphanBlocks = 100

// OrphanExpiry is the maximum age of an orphan block before it is eligible
// for expiry-based eviction, matching Bitcoin Core's ORPHAN_TX_EXPIRE_TIME
// concept adapted for blocks.
const OrphanExpiry = 20 * time.Minute

type orphanBlock struct {
	block    *types.Block
	addedAt  time.Time
}

// ErrSideChain is returned when a block is stored but not on the active chain.
// Callers can use errors.Is to distinguish this from validation failures.
var ErrSideChain = errors.New("side chain block")

// ErrOrphanBlock is returned when a block's parent is not known. The block is
// stored in the orphan pool and will be processed when its parent arrives.
var ErrOrphanBlock = errors.New("orphan block")

// ErrReorgTooDeep is returned when a reorg exceeds MaxReorgDepth. The
// competing chain is stored as a side chain but not activated.
var ErrReorgTooDeep = errors.New("reorg exceeds maximum depth")

// TimeSource provides network-adjusted time. Implementations should return a
// Unix timestamp that accounts for the median clock offset of connected peers,
// matching Bitcoin Core's GetAdjustedTime().
type TimeSource interface {
	Now() int64
}

// localClock is a fallback TimeSource that returns the raw local system time.
type localClock struct{}

func (localClock) Now() int64 { return time.Now().Unix() }

type Chain struct {
	mu sync.RWMutex

	params     *params.ChainParams
	engine     consensus.Engine
	store      store.BlockStore
	timeSource TimeSource

	tipHash   types.Hash
	tipHeight uint32
	tipWork   *big.Int

	heightByHash map[types.Hash]uint32
	hashByHeight map[uint32]types.Hash

	orphans map[types.Hash]*orphanBlock

	utxoSet *utxo.Set
}

func New(p *params.ChainParams, engine consensus.Engine, s store.BlockStore, ts TimeSource) *Chain {
	if ts == nil {
		ts = localClock{}
	}
	return &Chain{
		params:       p,
		engine:       engine,
		store:        s,
		timeSource:   ts,
		tipWork:      big.NewInt(0),
		heightByHash: make(map[types.Hash]uint32),
		hashByHeight: make(map[uint32]types.Hash),
		orphans:      make(map[types.Hash]*orphanBlock),
		utxoSet:      utxo.NewSet(),
	}
}

func (c *Chain) UtxoSet() *utxo.Set {
	return c.utxoSet
}

// Init loads the chain state from storage, or initializes with the genesis block.
// With the new persistent chainstate, UTXO set is loaded from LevelDB rather than
// replayed from blocks.
func (c *Chain) Init() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	tipHash, tipHeight, err := c.store.GetChainTip()
	if err != nil {
		return c.initGenesis()
	}

	c.tipHash = tipHash
	c.tipHeight = tipHeight

	// Rebuild in-memory index from the block index.
	c.tipWork = big.NewInt(0)
	if err := c.store.ForEachBlockIndex(func(hash types.Hash, rec *store.DiskBlockIndex) error {
		if rec.Status&store.StatusHaveData != 0 {
			c.heightByHash[hash] = rec.Height
		}
		return nil
	}); err != nil {
		return fmt.Errorf("rebuild index from block index: %w", err)
	}

	// Build hashByHeight for the main chain by walking backwards from tip.
	current := tipHash
	for h := tipHeight; ; {
		c.hashByHeight[h] = current
		rec, err := c.store.GetBlockIndex(current)
		if err != nil {
			return fmt.Errorf("walk chain at height %d: %w", h, err)
		}
		c.tipWork.Add(c.tipWork, store.CalcWork(rec.Header.Bits))
		if h == 0 {
			break
		}
		current = rec.Header.PrevBlock
		h--
	}

	// Load UTXO set from persistent chainstate.
	bestBlock, err := c.store.GetBestBlock()
	if err != nil {
		// Chainstate empty — need to rebuild from blocks.
		logging.L.Info("chainstate empty, rebuilding UTXO set from blocks", "component", "chain")
		return c.rebuildUtxoSet()
	}

	if bestBlock != tipHash {
		// Chainstate is ahead of block index tip — this happens when a crash
		// occurred after UTXO persistence but before PutChainTip. Advance the
		// block index tip to match chainstate rather than rebuilding.
		if bestBlockHeight, ok := c.heightByHash[bestBlock]; ok && bestBlockHeight > tipHeight {
			logging.L.Warn("chainstate ahead of chain tip, advancing tip", "component", "chain",
				"chainstate_tip", bestBlock.ReverseString(), "chainstate_height", bestBlockHeight,
				"chain_tip", tipHash.ReverseString(), "chain_height", tipHeight)

			// Rebuild hashByHeight from bestBlock backwards.
			c.hashByHeight = make(map[uint32]types.Hash)
			current := bestBlock
			c.tipWork = big.NewInt(0)
			for h := bestBlockHeight; ; {
				c.hashByHeight[h] = current
				rec, err := c.store.GetBlockIndex(current)
				if err != nil {
					return fmt.Errorf("walk chain from chainstate tip at height %d: %w", h, err)
				}
				c.tipWork.Add(c.tipWork, store.CalcWork(rec.Header.Bits))
				if h == 0 {
					break
				}
				current = rec.Header.PrevBlock
				h--
			}

			c.tipHash = bestBlock
			c.tipHeight = bestBlockHeight
		if err := c.store.PutChainTip(bestBlock, bestBlockHeight); err != nil {
			return fmt.Errorf("advance chain tip to chainstate: %w", err)
		}
		if err := c.loadUtxoSetFromChainstate(); err != nil {
			logging.L.Warn("failed to load UTXO set after tip advance, rebuilding", "component", "chain", "error", err)
			return c.rebuildUtxoSet()
		}
		logging.L.Info("chain tip advanced to match chainstate", "component", "chain",
			"height", bestBlockHeight, "utxos", c.utxoSet.Count())
		return nil
		}

		logging.L.Warn("chainstate tip mismatch, rebuilding UTXO set", "component", "chain",
			"chainstate_tip", bestBlock.ReverseString(), "chain_tip", tipHash.ReverseString())
		return c.rebuildUtxoSet()
	}

	// Chainstate is consistent — load UTXOs into in-memory set.
	if err := c.loadUtxoSetFromChainstate(); err != nil {
		logging.L.Warn("failed to load UTXO set from chainstate, rebuilding", "component", "chain", "error", err)
		return c.rebuildUtxoSet()
	}
	logging.L.Info("chain loaded from persistent storage", "component", "chain",
		"height", tipHeight, "utxos", c.utxoSet.Count())
	return nil
}

// loadUtxoSetFromChainstate populates the in-memory UTXO set from the persistent
// chainstate DB. Called during Init() when chainstate is consistent with the chain tip.
func (c *Chain) loadUtxoSetFromChainstate() error {
	return c.store.ForEachUtxo(func(txHash types.Hash, index uint32, data []byte) error {
		entry, err := utxo.DeserializeUtxoEntry(data)
		if err != nil {
			return fmt.Errorf("deserialize UTXO %s:%d: %w", txHash, index, err)
		}
		c.utxoSet.Add(txHash, index, entry)
		return nil
	})
}

// rebuildUtxoSet replays all blocks to reconstruct the UTXO set and persist it.
func (c *Chain) rebuildUtxoSet() error {
	for h := uint32(0); h <= c.tipHeight; h++ {
		hash, ok := c.hashByHeight[h]
		if !ok {
			return fmt.Errorf("missing hash at height %d during UTXO rebuild", h)
		}
		block, err := c.store.GetBlock(hash)
		if err != nil {
			return fmt.Errorf("load block at height %d for UTXO rebuild: %w", h, err)
		}
		if h == 0 {
			if err := c.utxoSet.ConnectGenesis(block); err != nil {
				return fmt.Errorf("connect genesis UTXOs: %w", err)
			}
		} else {
			if _, err := c.utxoSet.ConnectBlock(block, h); err != nil {
				return fmt.Errorf("connect block %d UTXOs: %w", h, err)
			}
		}
	}

	// Persist the rebuilt UTXO set to chainstate.
	if err := c.flushUtxoSetToChainstate(); err != nil {
		return fmt.Errorf("flush UTXO set to chainstate: %w", err)
	}

	logging.L.Info("UTXO set rebuilt and persisted", "component", "chain",
		"utxos", c.utxoSet.Count(), "height", c.tipHeight)
	return nil
}

// flushUtxoSetToChainstate writes the entire in-memory UTXO set to the chainstate DB.
func (c *Chain) flushUtxoSetToChainstate() error {
	wb := c.store.NewUtxoWriteBatch()
	c.utxoSet.ForEach(func(txHash types.Hash, index uint32, entry *utxo.UtxoEntry) {
		wb.PutUtxo(txHash, index, entry.Serialize())
	})
	wb.PutBestBlock(c.tipHash)
	return c.store.FlushUtxoBatch(wb)
}

func (c *Chain) initGenesis() error {
	genesisHash := crypto.HashBlockHeader(&c.params.GenesisBlock.Header)
	if !c.params.GenesisHash.IsZero() && genesisHash != c.params.GenesisHash {
		return fmt.Errorf("genesis hash mismatch: computed=%s expected=%s", genesisHash, c.params.GenesisHash)
	}

	// Write genesis block to flat files.
	fileNum, offset, size, err := c.store.WriteBlock(genesisHash, &c.params.GenesisBlock)
	if err != nil {
		return fmt.Errorf("store genesis block: %w", err)
	}

	// Create block index entry.
	genesisWork := store.CalcWork(c.params.GenesisBlock.Header.Bits)
	rec := &store.DiskBlockIndex{
		Header:    c.params.GenesisBlock.Header,
		Height:    0,
		Status:    store.StatusHaveData | store.StatusValidHeader | store.StatusValidTx,
		TxCount:   uint32(len(c.params.GenesisBlock.Transactions)),
		FileNum:   fileNum,
		DataPos:   offset,
		DataSize:  size,
		ChainWork: genesisWork,
	}
	if err := c.store.PutBlockIndex(genesisHash, rec); err != nil {
		return fmt.Errorf("store genesis index: %w", err)
	}
	if err := c.store.PutChainTip(genesisHash, 0); err != nil {
		return fmt.Errorf("store genesis tip: %w", err)
	}

	c.tipHash = genesisHash
	c.tipHeight = 0
	c.tipWork = genesisWork
	c.heightByHash[genesisHash] = 0
	c.hashByHeight[0] = genesisHash

	if err := c.utxoSet.ConnectGenesis(&c.params.GenesisBlock); err != nil {
		return fmt.Errorf("connect genesis UTXOs: %w", err)
	}

	// Persist genesis UTXO to chainstate.
	if err := c.flushUtxoSetToChainstate(); err != nil {
		return fmt.Errorf("flush genesis UTXO: %w", err)
	}

	return nil
}

func (c *Chain) Tip() (types.Hash, uint32) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.tipHash, c.tipHeight
}

func (c *Chain) TipHeader() (*types.BlockHeader, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.store.GetHeader(c.tipHash)
}

func (c *Chain) GetHeaderByHeight(height uint32) (*types.BlockHeader, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	hash, ok := c.hashByHeight[height]
	if !ok {
		return nil, fmt.Errorf("no block at height %d", height)
	}
	return c.store.GetHeader(hash)
}

func (c *Chain) GetBlockByHeight(height uint32) (*types.Block, types.Hash, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	hash, ok := c.hashByHeight[height]
	if !ok {
		return nil, types.ZeroHash, fmt.Errorf("no block at height %d", height)
	}
	block, err := c.store.GetBlock(hash)
	if err != nil {
		return nil, types.ZeroHash, err
	}
	return block, hash, nil
}

func (c *Chain) GetAncestor(height uint32) *types.BlockHeader {
	c.mu.RLock()
	defer c.mu.RUnlock()
	hash, ok := c.hashByHeight[height]
	if !ok {
		return nil
	}
	h, err := c.store.GetHeader(hash)
	if err != nil {
		return nil
	}
	return h
}

// GetBlockHeight returns the height of a block on the active chain.
func (c *Chain) GetBlockHeight(hash types.Hash) (uint32, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	height, ok := c.heightByHash[hash]
	if !ok {
		return 0, fmt.Errorf("block %s not on active chain", hash.ReverseString())
	}
	return height, nil
}

// BlockLocator returns a list of block hashes from the tip of the active chain,
// spaced exponentially like Bitcoin Core's CChain::GetLocator(). The result is:
// tip, tip-1, tip-2, tip-4, tip-8, ..., genesis. This allows a peer to find the
// highest common block even when chains have diverged significantly.
func (c *Chain) BlockLocator() []types.Hash {
	c.mu.RLock()
	defer c.mu.RUnlock()

	var locator []types.Hash
	step := uint32(1)
	height := c.tipHeight

	for {
		hash, ok := c.hashByHeight[height]
		if !ok {
			break
		}
		locator = append(locator, hash)

		if height == 0 {
			break
		}

		if height < step {
			height = 0
		} else {
			height -= step
		}

		// After the first 10 entries (which are consecutive), start doubling
		// the step size. This matches Bitcoin Core's locator construction.
		if len(locator) > 10 {
			step *= 2
		}
	}

	return locator
}

// FindMainChainHash checks if a hash is on the active main chain and returns
// its height. Returns (height, true) if found, (0, false) otherwise.
func (c *Chain) FindMainChainHash(hash types.Hash) (uint32, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	height, ok := c.heightByHash[hash]
	if !ok {
		return 0, false
	}
	mainHash, exists := c.hashByHeight[height]
	if !exists || mainHash != hash {
		return 0, false
	}
	return height, true
}

func (c *Chain) HasBlock(hash types.Hash) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if _, ok := c.heightByHash[hash]; ok {
		return true
	}
	if _, ok := c.orphans[hash]; ok {
		return true
	}
	has, _ := c.store.HasBlock(hash)
	return has
}

// HasBlockOnChain returns true only if the block is tracked in memory as part
// of a known chain (main or side). Blocks that are only in the orphan pool or
// only in the disk index are NOT considered "on chain". This is used by the P2P
// layer to decide whether to request a block from a peer — orphans and rejected
// blocks must remain requestable so the node can converge after reorgs.
func (c *Chain) HasBlockOnChain(hash types.Hash) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	_, ok := c.heightByHash[hash]
	return ok
}

func (c *Chain) ProcessBlock(block *types.Block) (uint32, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	blockHash := crypto.HashBlockHeader(&block.Header)

	if logging.DebugMode {
		logging.L.Debug("[dbg] chain.ProcessBlock enter",
			"hash", blockHash.ReverseString()[:16],
			"parent", block.Header.PrevBlock.ReverseString()[:16],
			"tip", c.tipHash.ReverseString()[:16],
			"tip_height", c.tipHeight)
	}

	if _, ok := c.heightByHash[blockHash]; ok {
		metrics.Global.BlocksRejected.Add(1)
		return 0, fmt.Errorf("block %s already in chain", blockHash)
	}
	if _, ok := c.orphans[blockHash]; ok {
		metrics.Global.BlocksRejected.Add(1)
		return 0, fmt.Errorf("%w: block %s already in orphan pool", ErrOrphanBlock, blockHash)
	}

	parentHeight, parentKnown := c.heightByHash[block.Header.PrevBlock]
	if !parentKnown {
		// Lightweight PoW sanity check before accepting into the orphan pool.
		// We can't verify the difficulty is correct (unknown parent), but we
		// can verify the header hash meets its own declared target. This
		// prevents zero-cost orphan flooding since the attacker must perform
		// real PoW for every orphan they submit.
		orphanHash := crypto.HashBlockHeader(&block.Header)
		if err := crypto.ValidateProofOfWork(orphanHash, block.Header.Bits); err != nil {
			metrics.Global.BlocksRejected.Add(1)
			return 0, fmt.Errorf("orphan PoW check failed: %w", err)
		}

		c.evictExpiredOrphans()
		if len(c.orphans) >= MaxOrphanBlocks {
			c.evictRandomOrphan()
		}
		c.orphans[blockHash] = &orphanBlock{block: block, addedAt: time.Now()}
		metrics.Global.OrphansReceived.Add(1)
		if logging.DebugMode {
			logging.L.Debug("[dbg] chain.ProcessBlock → orphan",
				"hash", blockHash.ReverseString()[:16],
				"parent", block.Header.PrevBlock.ReverseString()[:16],
				"orphan_pool_size", len(c.orphans))
		}
		return 0, fmt.Errorf("%w: %s (parent %s unknown)", ErrOrphanBlock, blockHash, block.Header.PrevBlock)
	}

	newHeight := parentHeight + 1

	parentHeader, err := c.store.GetHeader(block.Header.PrevBlock)
	if err != nil {
		return 0, fmt.Errorf("load parent header: %w", err)
	}

	// Build a side-chain-aware ancestor lookup for this block's parent chain.
	// This is critical: getAncestorUnsafe only returns active main-chain blocks,
	// which produces wrong difficulty at retarget boundaries for side chains.
	getAncestor := c.buildAncestorLookup(block.Header.PrevBlock, parentHeight)

	nowUnix := uint32(c.timeSource.Now())
	if err := c.engine.ValidateHeader(&block.Header, parentHeader, newHeight, getAncestor, c.params); err != nil {
		return 0, fmt.Errorf("validate header at height %d: %w", newHeight, err)
	}

	if err := consensus.ValidateHeaderTimestamp(&block.Header, parentHeader, nowUnix, getAncestor, parentHeight, c.params); err != nil {
		return 0, fmt.Errorf("validate timestamp: %w", err)
	}

	if err := c.engine.ValidateBlock(block, newHeight, c.params); err != nil {
		return 0, fmt.Errorf("validate block at height %d: %w", newHeight, err)
	}

	blockWork := crypto.CalcWork(block.Header.Bits)
	parentWork, err := c.workForParentChain(block.Header.PrevBlock)
	if err != nil {
		return 0, fmt.Errorf("compute parent chain work: %w", err)
	}
	newWork := new(big.Int).Add(parentWork, blockWork)

	// Write block to flat files.
	fileNum, offset, size, err := c.store.WriteBlock(blockHash, block)
	if err != nil {
		return 0, fmt.Errorf("store block: %w", err)
	}

	// Create block index entry.
	rec := &store.DiskBlockIndex{
		Header:    block.Header,
		Height:    newHeight,
		Status:    store.StatusHaveData | store.StatusValidHeader,
		TxCount:   uint32(len(block.Transactions)),
		FileNum:   fileNum,
		DataPos:   offset,
		DataSize:  size,
		ChainWork: newWork,
	}

	if block.Header.PrevBlock == c.tipHash {
		if _, err := consensus.ValidateTransactionInputs(block, c.utxoSet, newHeight, c.params); err != nil {
			metrics.Global.BlocksRejected.Add(1)
			return 0, fmt.Errorf("validate tx inputs at height %d: %w", newHeight, err)
		}

		undoData, err := c.utxoSet.ConnectBlock(block, newHeight)
		if err != nil {
			metrics.Global.BlocksRejected.Add(1)
			return 0, fmt.Errorf("connect block UTXOs: %w", err)
		}

		undoBytes := utxo.SerializeUndoData(undoData)
		undoOffset, undoSize, err := c.store.WriteUndo(fileNum, undoBytes)
		if err != nil {
			c.utxoSet.DisconnectBlock(block, undoData)
			return 0, fmt.Errorf("store undo data: %w", err)
		}
		rec.UndoFile = fileNum
		rec.UndoPos = undoOffset
		rec.UndoSize = undoSize
		rec.Status |= store.StatusHaveUndo | store.StatusValidTx

		if err := c.store.PutBlockIndex(blockHash, rec); err != nil {
			c.utxoSet.DisconnectBlock(block, undoData)
			return 0, fmt.Errorf("store block index: %w", err)
		}

		// Persist UTXO changes BEFORE updating chain tip. If this fails, roll
		// back in-memory UTXO state so the node never operates with an advanced
		// UTXO set that isn't persisted. On crash between UTXO flush and chain
		// tip write, Init() detects the mismatch and advances the tip.
		if err := c.persistUtxoChanges(block, undoData, blockHash); err != nil {
			c.utxoSet.DisconnectBlock(block, undoData)
			return 0, fmt.Errorf("persist UTXO changes: %w", err)
		}

		if err := c.extendChain(blockHash, newHeight, newWork); err != nil {
			return 0, err
		}
		metrics.Global.BlocksAccepted.Add(1)
		if logging.DebugMode {
			logging.L.Debug("[dbg] chain.ProcessBlock → extend main chain",
				"hash", blockHash.ReverseString()[:16],
				"height", newHeight)
		}

	} else if newWork.Cmp(c.tipWork) > 0 || (newWork.Cmp(c.tipWork) == 0 && bytes.Compare(blockHash[:], c.tipHash[:]) < 0) {
		if err := c.store.PutBlockIndex(blockHash, rec); err != nil {
			return 0, fmt.Errorf("store block index: %w", err)
		}
		if logging.DebugMode {
			logging.L.Debug("[dbg] chain.ProcessBlock → REORG triggered",
				"hash", blockHash.ReverseString()[:16],
				"new_height", newHeight,
				"new_work", newWork.Text(16),
				"old_tip", c.tipHash.ReverseString()[:16],
				"old_height", c.tipHeight,
				"old_work", c.tipWork.Text(16))
		}
		if err := c.reorg(blockHash, newHeight, newWork); err != nil {
			if errors.Is(err, ErrReorgTooDeep) {
				c.heightByHash[blockHash] = newHeight
				logging.L.Warn("reorg rejected (too deep), storing as side chain",
					"component", "chain", "hash", blockHash.ReverseString()[:16], "height", newHeight, "error", err)
				c.processOrphans(blockHash)
				return newHeight, fmt.Errorf("%w: %s", ErrSideChain, err)
			}
			return 0, fmt.Errorf("reorg to %s: %w", blockHash, err)
		}
		metrics.Global.BlocksAccepted.Add(1)
	} else {
		if err := c.store.PutBlockIndex(blockHash, rec); err != nil {
			return 0, fmt.Errorf("store block index: %w", err)
		}
		c.heightByHash[blockHash] = newHeight
		if logging.DebugMode {
			logging.L.Debug("[dbg] chain.ProcessBlock → side chain (insufficient work)",
				"hash", blockHash.ReverseString()[:16],
				"height", newHeight,
				"block_work", newWork.Text(16),
				"tip_work", c.tipWork.Text(16))
		}
		c.processOrphans(blockHash)
		return newHeight, fmt.Errorf("%w: block %s at height %d (insufficient work)", ErrSideChain, blockHash, newHeight)
	}

	c.processOrphans(blockHash)

	return newHeight, nil
}

// persistUtxoChanges writes UTXO changes for a connected block to the chainstate DB.
// Modeled after Bitcoin Core's FlushStateToDisk: all UTXO mutations and the best-block
// pointer are written in a single atomic LevelDB batch so a crash at any point leaves
// the chainstate either fully at the old tip or fully at the new tip.
func (c *Chain) persistUtxoChanges(block *types.Block, undoData *utxo.BlockUndoData, blockHash types.Hash) error {
	wb := c.store.NewUtxoWriteBatch()

	for _, tx := range block.Transactions {
		txHash, err := crypto.HashTransaction(&tx)
		if err != nil {
			return fmt.Errorf("hash tx during UTXO persist: %w", err)
		}
		for i := range tx.Outputs {
			entry := c.utxoSet.Get(txHash, uint32(i))
			if entry != nil {
				wb.PutUtxo(txHash, uint32(i), entry.Serialize())
			}
		}
	}

	if undoData != nil {
		for _, spent := range undoData.SpentOutputs {
			wb.DeleteUtxo(spent.OutPoint.Hash, spent.OutPoint.Index)
		}
	}

	wb.PutBestBlock(blockHash)
	return c.store.FlushUtxoBatch(wb)
}

// persistUtxoDisconnect writes the UTXO changes for a disconnected block: re-adds
// spent outputs and removes the outputs that were created by the block.
func (c *Chain) persistUtxoDisconnect(block *types.Block, undoData *utxo.BlockUndoData, newBestHash types.Hash) error {
	wb := c.store.NewUtxoWriteBatch()

	for _, tx := range block.Transactions {
		txHash, err := crypto.HashTransaction(&tx)
		if err != nil {
			return fmt.Errorf("hash tx during UTXO disconnect persist: %w", err)
		}
		for i := range tx.Outputs {
			wb.DeleteUtxo(txHash, uint32(i))
		}
	}

	for _, spent := range undoData.SpentOutputs {
		wb.PutUtxo(spent.OutPoint.Hash, spent.OutPoint.Index, spent.Entry.Serialize())
	}

	wb.PutBestBlock(newBestHash)
	return c.store.FlushUtxoBatch(wb)
}

func (c *Chain) extendChain(hash types.Hash, height uint32, work *big.Int) error {
	if err := c.store.PutChainTip(hash, height); err != nil {
		return err
	}
	c.heightByHash[hash] = height
	c.hashByHeight[height] = hash
	c.tipHash = hash
	c.tipHeight = height
	c.tipWork = work
	return nil
}

func (c *Chain) reorg(newTipHash types.Hash, newTipHeight uint32, newWork *big.Int) (err error) {
	// Snapshot in-memory state so reorg failures cannot poison the node.
	heightByHashSnapshot := make(map[types.Hash]uint32, len(c.heightByHash))
	for h, v := range c.heightByHash {
		heightByHashSnapshot[h] = v
	}
	hashByHeightSnapshot := make(map[uint32]types.Hash, len(c.hashByHeight))
	for h, v := range c.hashByHeight {
		hashByHeightSnapshot[h] = v
	}
	tipHashSnapshot := c.tipHash
	tipHeightSnapshot := c.tipHeight
	tipWorkSnapshot := new(big.Int).Set(c.tipWork)
	utxoSnapshot := cloneUtxoSet(c.utxoSet)

	restoreSnapshot := func() {
		c.heightByHash = heightByHashSnapshot
		c.hashByHeight = hashByHeightSnapshot
		c.tipHash = tipHashSnapshot
		c.tipHeight = tipHeightSnapshot
		c.tipWork = tipWorkSnapshot
		c.utxoSet = utxoSnapshot
	}
	defer func() {
		if err != nil {
			restoreSnapshot()
		}
	}()

	newChain := []types.Hash{newTipHash}
	current := newTipHash

	for {
		block, err := c.store.GetBlock(current)
		if err != nil {
			return fmt.Errorf("load block %s during reorg: %w", current, err)
		}
		prevHash := block.Header.PrevBlock

		if parentHeight, ok := c.heightByHash[prevHash]; ok {
			if c.hashByHeight[parentHeight] == prevHash {
				break
			}
		}

		newChain = append(newChain, prevHash)
		current = prevHash
	}

	forkBlock, _ := c.store.GetBlock(newChain[len(newChain)-1])
	forkParentHeight := c.heightByHash[forkBlock.Header.PrevBlock]

	oldTipHeight := c.tipHeight
	reorgDepth := oldTipHeight - forkParentHeight

	if c.params.MaxReorgDepth > 0 && reorgDepth > c.params.MaxReorgDepth {
		return fmt.Errorf("%w: depth %d exceeds limit %d (fork at height %d)",
			ErrReorgTooDeep, reorgDepth, c.params.MaxReorgDepth, forkParentHeight)
	}

	logging.L.Warn("chain reorg", "component", "chain", "fork_height", forkParentHeight, "old_tip", oldTipHeight, "new_tip", newTipHeight, "depth", reorgDepth)
	metrics.Global.Reorgs.Add(1)
	metrics.Global.ReorgDepthTotal.Add(uint64(reorgDepth))

	if logging.DebugMode {
		logging.L.Debug("[dbg] reorg detail",
			"old_tip_hash", c.tipHash.ReverseString()[:16],
			"new_tip_hash", newTipHash.ReverseString()[:16],
			"fork_height", forkParentHeight,
			"disconnect_count", reorgDepth,
			"connect_count", len(newChain))
	}

	// Disconnect old main chain blocks (UTXO rollback).
	// Each disconnect is flushed to chainstate incrementally so a crash
	// mid-reorg leaves the chainstate consistent at a valid intermediate tip.
	for h := c.tipHeight; h > forkParentHeight; h-- {
		hash, ok := c.hashByHeight[h]
		if !ok {
			continue
		}
		block, err := c.store.GetBlock(hash)
		if err != nil {
			return fmt.Errorf("load block at height %d for disconnect: %w", h, err)
		}

		rec, err := c.store.GetBlockIndex(hash)
		if err != nil {
			return fmt.Errorf("load block index at height %d: %w", h, err)
		}
		if rec.Status&store.StatusHaveUndo == 0 {
			return fmt.Errorf("no undo data for block at height %d", h)
		}
		undoBytes, err := c.store.ReadUndo(rec.UndoFile, rec.UndoPos, rec.UndoSize)
		if err != nil {
			return fmt.Errorf("read undo data at height %d: %w", h, err)
		}
		undoData, err := utxo.DeserializeUndoData(undoBytes)
		if err != nil {
			return fmt.Errorf("deserialize undo data at height %d: %w", h, err)
		}
		if err := c.utxoSet.DisconnectBlock(block, undoData); err != nil {
			return fmt.Errorf("disconnect block at height %d: %w", h, err)
		}

		parentHash := block.Header.PrevBlock
		if err := c.persistUtxoDisconnect(block, undoData, parentHash); err != nil {
			return fmt.Errorf("persist disconnect at height %d: %w", h, err)
		}
	}

	// Remove old main chain entries above fork.
	for h := forkParentHeight + 1; h <= c.tipHeight; h++ {
		if hash, ok := c.hashByHeight[h]; ok {
			delete(c.heightByHash, hash)
			delete(c.hashByHeight, h)
		}
	}

	type reorgBlockEntry struct {
		hash     types.Hash
		height   uint32
		undoData *utxo.BlockUndoData
		block    *types.Block
	}
	connected := make([]reorgBlockEntry, 0, len(newChain))

	// Connect new chain blocks and validate transactions.
	for i := len(newChain) - 1; i >= 0; i-- {
		h := forkParentHeight + uint32(len(newChain)-i)
		blockHash := newChain[i]
		block, err := c.store.GetBlock(blockHash)
		if err != nil {
			return fmt.Errorf("load new chain block %s: %w", blockHash, err)
		}

		if _, err := consensus.ValidateTransactionInputs(block, c.utxoSet, h, c.params); err != nil {
			return fmt.Errorf("validate tx inputs during reorg at height %d: %w", h, err)
		}

		undoData, err := c.utxoSet.ConnectBlock(block, h)
		if err != nil {
			return fmt.Errorf("connect block %s UTXOs during reorg: %w", blockHash, err)
		}

		if err := c.persistUtxoChanges(block, undoData, blockHash); err != nil {
			return fmt.Errorf("persist UTXO connect during reorg at height %d: %w", h, err)
		}

		connected = append(connected, reorgBlockEntry{hash: blockHash, height: h, undoData: undoData, block: block})
		c.heightByHash[blockHash] = h
		c.hashByHeight[h] = blockHash
	}

	// Write chain tip immediately after all connects are persisted to
	// chainstate. A crash between the last persistUtxoChanges and this
	// PutChainTip is safe: Init() detects that chainstate bestBlock is
	// ahead of the block index tip and advances the tip to match.
	if err := c.store.PutChainTip(newTipHash, newTipHeight); err != nil {
		return err
	}

	c.tipHash = newTipHash
	c.tipHeight = newTipHeight
	c.tipWork = newWork

	// Persist undo data and block index entries. These are not consensus-
	// critical for the current tip — they enable future disconnects. A crash
	// here leaves the chain tip and chainstate consistent; undo data for
	// these blocks will be regenerated if needed.
	for _, entry := range connected {
		rec, err := c.store.GetBlockIndex(entry.hash)
		if err != nil {
			return fmt.Errorf("get block index for %s: %w", entry.hash, err)
		}
		undoBytes := utxo.SerializeUndoData(entry.undoData)
		undoOffset, undoSize, err := c.store.WriteUndo(rec.FileNum, undoBytes)
		if err != nil {
			return fmt.Errorf("write undo data during reorg: %w", err)
		}
		rec.UndoFile = rec.FileNum
		rec.UndoPos = undoOffset
		rec.UndoSize = undoSize
		rec.Status |= store.StatusHaveUndo | store.StatusValidTx
		if err := c.store.PutBlockIndex(entry.hash, rec); err != nil {
			return fmt.Errorf("update block index during reorg: %w", err)
		}
	}

	return nil
}

func cloneUtxoSet(src *utxo.Set) *utxo.Set {
	dst := utxo.NewSet()
	for key, entry := range src.Entries() {
		var txHash types.Hash
		copy(txHash[:], key[:32])
		index := binary.LittleEndian.Uint32(key[32:])
		entryCopy := *entry
		entryCopy.PkScript = make([]byte, len(entry.PkScript))
		copy(entryCopy.PkScript, entry.PkScript)
		dst.Add(txHash, index, &entryCopy)
	}
	return dst
}

// evictExpiredOrphans removes orphans older than OrphanExpiry.
func (c *Chain) evictExpiredOrphans() {
	now := time.Now()
	for hash, ob := range c.orphans {
		if now.Sub(ob.addedAt) > OrphanExpiry {
			delete(c.orphans, hash)
		}
	}
}

// evictRandomOrphan removes one random orphan to make room for a new one.
// Uses map iteration order which is randomized in Go.
func (c *Chain) evictRandomOrphan() {
	for hash := range c.orphans {
		logging.L.Debug("evicting orphan to make room", "component", "chain",
			"evicted", hash.ReverseString())
		delete(c.orphans, hash)
		return
	}
}

type orphanEntry struct {
	hash  types.Hash
	block *types.Block
}

func (c *Chain) processOrphans(parentHash types.Hash) {
	queue := []types.Hash{parentHash}

	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]

		var toProcess []orphanEntry
		for hash, ob := range c.orphans {
			if ob.block.Header.PrevBlock == parent {
				toProcess = append(toProcess, orphanEntry{hash: hash, block: ob.block})
				delete(c.orphans, hash)
			}
		}

		if logging.DebugMode && len(toProcess) > 0 {
			logging.L.Debug("[dbg] processOrphans",
				"parent", parent.ReverseString()[:16],
				"candidates", len(toProcess),
				"remaining_orphans", len(c.orphans))
		}

		sort.Slice(toProcess, func(i, j int) bool {
			return bytes.Compare(toProcess[i].hash[:], toProcess[j].hash[:]) < 0
		})

		for _, entry := range toProcess {
			blockHash := entry.hash
			orphan := entry.block

			parentHeight, ok := c.heightByHash[orphan.Header.PrevBlock]
			if !ok {
				continue
			}
			newHeight := parentHeight + 1
			parentHeader, err := c.store.GetHeader(orphan.Header.PrevBlock)
			if err != nil {
				continue
			}

			getAncestor := c.buildAncestorLookup(orphan.Header.PrevBlock, parentHeight)

			if err := c.engine.ValidateHeader(&orphan.Header, parentHeader, newHeight, getAncestor, c.params); err != nil {
				logging.L.Debug("orphan failed header validation", "component", "chain", "hash", blockHash.ReverseString(), "error", err)
				continue
			}

			nowUnix := uint32(c.timeSource.Now())
			if err := consensus.ValidateHeaderTimestamp(&orphan.Header, parentHeader, nowUnix, getAncestor, parentHeight, c.params); err != nil {
				logging.L.Debug("orphan failed timestamp validation", "component", "chain", "hash", blockHash.ReverseString(), "error", err)
				continue
			}

			if err := c.engine.ValidateBlock(orphan, newHeight, c.params); err != nil {
				logging.L.Debug("orphan failed block validation", "component", "chain", "hash", blockHash.ReverseString(), "error", err)
				continue
			}

			blockWork := crypto.CalcWork(orphan.Header.Bits)
			parentWork, err := c.workForParentChain(orphan.Header.PrevBlock)
			if err != nil {
				logging.L.Debug("orphan parent work lookup failed", "component", "chain", "hash", blockHash.ReverseString(), "error", err)
				continue
			}
			newWork := new(big.Int).Add(parentWork, blockWork)

			fileNum, offset, size, err := c.store.WriteBlock(blockHash, orphan)
			if err != nil {
				continue
			}
			rec := &store.DiskBlockIndex{
				Header:    orphan.Header,
				Height:    newHeight,
				Status:    store.StatusHaveData | store.StatusValidHeader,
				TxCount:   uint32(len(orphan.Transactions)),
				FileNum:   fileNum,
				DataPos:   offset,
				DataSize:  size,
				ChainWork: newWork,
			}

			if orphan.Header.PrevBlock == c.tipHash {
				if _, err := consensus.ValidateTransactionInputs(orphan, c.utxoSet, newHeight, c.params); err != nil {
					logging.L.Debug("orphan failed tx input validation", "component", "chain", "hash", blockHash.ReverseString(), "error", err)
					continue
				}
				undoData, err := c.utxoSet.ConnectBlock(orphan, newHeight)
				if err != nil {
					logging.L.Warn("orphan UTXO connect failed", "component", "chain", "hash", blockHash.ReverseString(), "error", err)
					continue
				}
				undoBytes := utxo.SerializeUndoData(undoData)
				undoOffset, undoSize, wErr := c.store.WriteUndo(fileNum, undoBytes)
				if wErr == nil {
					rec.UndoFile = fileNum
					rec.UndoPos = undoOffset
					rec.UndoSize = undoSize
					rec.Status |= store.StatusHaveUndo | store.StatusValidTx
				}
				if err := c.store.PutBlockIndex(blockHash, rec); err != nil {
					c.utxoSet.DisconnectBlock(orphan, undoData)
					logging.L.Warn("orphan block index write failed", "component", "chain", "hash", blockHash.ReverseString(), "error", err)
					continue
				}

				if err := c.persistUtxoChanges(orphan, undoData, blockHash); err != nil {
					c.utxoSet.DisconnectBlock(orphan, undoData)
					logging.L.Error("orphan UTXO flush failed", "component", "chain", "hash", blockHash.ReverseString(), "error", err)
					continue
				}
				if err := c.extendChain(blockHash, newHeight, newWork); err != nil {
					logging.L.Warn("orphan extend failed", "component", "chain", "hash", blockHash.ReverseString(), "error", err)
					continue
				}
			} else if newWork.Cmp(c.tipWork) > 0 || (newWork.Cmp(c.tipWork) == 0 && bytes.Compare(blockHash[:], c.tipHash[:]) < 0) {
				_ = c.store.PutBlockIndex(blockHash, rec)
				c.heightByHash[blockHash] = newHeight
				if err := c.reorg(blockHash, newHeight, newWork); err != nil {
					if !errors.Is(err, ErrReorgTooDeep) {
						delete(c.heightByHash, blockHash)
					}
					logging.L.Warn("orphan reorg failed", "component", "chain", "hash", blockHash.ReverseString(), "error", err)
					continue
				}
				logging.L.Info("reorg from orphan resolution", "component", "chain", "hash", blockHash.ReverseString(), "height", newHeight)
				queue = []types.Hash{blockHash}
				break
			} else {
				_ = c.store.PutBlockIndex(blockHash, rec)
				c.heightByHash[blockHash] = newHeight
			}

			queue = append(queue, blockHash)
		}
	}
}

func (c *Chain) workForParentChain(blockHash types.Hash) (*big.Int, error) {
	rec, err := c.store.GetBlockIndex(blockHash)
	if err != nil {
		return nil, fmt.Errorf("load block index for %s: %w", blockHash, err)
	}
	if rec.ChainWork == nil {
		return nil, fmt.Errorf("block %s has nil ChainWork in index", blockHash)
	}
	return new(big.Int).Set(rec.ChainWork), nil
}

// getAncestorUnsafe returns the main chain block header at the given height.
// Only valid for blocks on the active main chain.
func (c *Chain) getAncestorUnsafe(height uint32) *types.BlockHeader {
	hash, ok := c.hashByHeight[height]
	if !ok {
		return nil
	}
	h, err := c.store.GetHeader(hash)
	if err != nil {
		return nil
	}
	return h
}

// buildAncestorLookup constructs a height->header map for a block's ancestor
// chain by walking backwards from parentHash through the store. This produces
// a correct ancestor function for side-chain blocks where the parent chain
// may differ from the active main chain (critical for difficulty retargeting).
func (c *Chain) buildAncestorLookup(parentHash types.Hash, parentHeight uint32) func(uint32) *types.BlockHeader {
	ancestors := make(map[uint32]*types.BlockHeader)

	current := parentHash
	h := parentHeight
	for {
		// If this height is on the main chain with the same hash, we can use
		// the main chain for this height and all below — they share history.
		if mainHash, ok := c.hashByHeight[h]; ok && mainHash == current {
			break
		}

		header, err := c.store.GetHeader(current)
		if err != nil {
			break
		}
		ancestors[h] = header

		if header.PrevBlock.IsZero() {
			break
		}
		current = header.PrevBlock
		if h == 0 {
			break
		}
		h--
	}

	return func(height uint32) *types.BlockHeader {
		if hdr, ok := ancestors[height]; ok {
			return hdr
		}
		return c.getAncestorUnsafe(height)
	}
}

func (c *Chain) GetBlock(hash types.Hash) (*types.Block, error) {
	return c.store.GetBlock(hash)
}

type ChainInfo struct {
	Network          string
	Height           uint32
	BestHash         types.Hash
	GenesisHash      types.Hash
	Bits             uint32
	Difficulty       float64
	Chainwork        *big.Int
	MedianTimePast   uint32
	RetargetEpoch    uint32
	EpochProgress    uint32
	EpochBlocksLeft  uint32
	RetargetInterval uint32
	VerificationProg float64
}

func (c *Chain) GetChainInfo() *ChainInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()

	info := &ChainInfo{
		Network:          c.params.Name,
		Height:           c.tipHeight,
		BestHash:         c.tipHash,
		GenesisHash:      c.params.GenesisHash,
		Chainwork:        new(big.Int).Set(c.tipWork),
		RetargetInterval: c.params.RetargetInterval,
	}

	tipHeader, err := c.store.GetHeader(c.tipHash)
	if err == nil {
		info.Bits = tipHeader.Bits
		target := crypto.CompactToBig(tipHeader.Bits)
		if target.Sign() > 0 {
			genesisTarget := crypto.CompactToBig(c.params.InitialBits)
			fDiff := new(big.Float).SetInt(genesisTarget)
			fDiff.Quo(fDiff, new(big.Float).SetInt(target))
			info.Difficulty, _ = fDiff.Float64()
		}
	}

	const medianCount = 11
	timestamps := make([]uint32, 0, medianCount)
	for i := uint32(0); i < medianCount && c.tipHeight >= i; i++ {
		h := c.tipHeight - i
		hash, ok := c.hashByHeight[h]
		if !ok {
			break
		}
		hdr, err := c.store.GetHeader(hash)
		if err != nil {
			break
		}
		timestamps = append(timestamps, hdr.Timestamp)
	}
	if len(timestamps) > 0 {
		for i := 1; i < len(timestamps); i++ {
			key := timestamps[i]
			j := i - 1
			for j >= 0 && timestamps[j] > key {
				timestamps[j+1] = timestamps[j]
				j--
			}
			timestamps[j+1] = key
		}
		info.MedianTimePast = timestamps[len(timestamps)/2]
	}

	if c.params.RetargetInterval > 0 {
		info.RetargetEpoch = c.tipHeight / c.params.RetargetInterval
		info.EpochProgress = c.tipHeight % c.params.RetargetInterval
		info.EpochBlocksLeft = c.params.RetargetInterval - info.EpochProgress
	}

	info.VerificationProg = 1.0
	return info
}

// TxOutSetInfo returns UTXO set statistics atomically with the current tip.
type TxOutSetInfoResult struct {
	Height     uint32
	BestHash   types.Hash
	TxOuts     int
	TotalValue uint64
}

func (c *Chain) TxOutSetInfo() *TxOutSetInfoResult {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return &TxOutSetInfoResult{
		Height:     c.tipHeight,
		BestHash:   c.tipHash,
		TxOuts:     c.utxoSet.Count(),
		TotalValue: c.utxoSet.TotalValue(),
	}
}
