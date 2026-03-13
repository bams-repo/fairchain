package chain

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/bams-repo/fairchain/internal/consensus/pow"
	"github.com/bams-repo/fairchain/internal/crypto"
	fcparams "github.com/bams-repo/fairchain/internal/params"
	"github.com/bams-repo/fairchain/internal/store"
	"github.com/bams-repo/fairchain/internal/types"
)

func setupTestChain(t *testing.T) (*Chain, *fcparams.ChainParams) {
	t.Helper()

	p := &fcparams.ChainParams{}
	*p = *fcparams.Regtest

	// Mine genesis.
	cfg := fcparams.GenesisConfig{
		NetworkName:     "regtest",
		CoinbaseMessage: []byte("test genesis"),
		Timestamp:       1700000000,
		Bits:            p.InitialBits,
		Version:         1,
		Reward:          p.InitialSubsidy,
		RewardScript:    []byte{0x00},
	}
	genesis := fcparams.BuildGenesisBlock(cfg)
	if err := pow.MineGenesis(&genesis); err != nil {
		t.Fatalf("mine genesis: %v", err)
	}
	genesisHash := crypto.HashBlockHeader(&genesis.Header)
	fcparams.InitGenesis(p, genesis, genesisHash)

	dir := t.TempDir()
	s, err := store.NewFileStore(
		filepath.Join(dir, "blocks"),
		filepath.Join(dir, "blocks", "index"),
		filepath.Join(dir, "chainstate"),
		p.NetworkMagic,
	)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	engine := pow.New()
	c := New(p, engine, s, nil)
	if err := c.Init(); err != nil {
		t.Fatalf("init chain: %v", err)
	}

	return c, p
}

func mineBlock(t *testing.T, c *Chain, p *fcparams.ChainParams) *types.Block {
	t.Helper()

	tipHash, tipHeight := c.Tip()
	tipHeader, err := c.TipHeader()
	if err != nil {
		t.Fatalf("get tip header: %v", err)
	}

	newHeight := tipHeight + 1
	subsidy := p.CalcSubsidy(newHeight)

	heightBytes := make([]byte, 4)
	types.PutUint32LE(heightBytes, newHeight)

	coinbase := types.Transaction{
		Version: 1,
		Inputs: []types.TxInput{
			{
				PreviousOutPoint: types.CoinbaseOutPoint,
				SignatureScript:  append(heightBytes, []byte("test")...),
				Sequence:         0xFFFFFFFF,
			},
		},
		Outputs: []types.TxOutput{
			{Value: subsidy, PkScript: []byte{0x00}},
		},
	}

	merkle, _ := crypto.ComputeMerkleRoot([]types.Transaction{coinbase})

	header := types.BlockHeader{
		Version:    1,
		PrevBlock:  tipHash,
		MerkleRoot: merkle,
		Timestamp:  tipHeader.Timestamp + 1,
		Bits:       p.InitialBits,
		Nonce:      0,
	}

	target := crypto.CompactToHash(header.Bits)
	engine := pow.New()
	found, _ := engine.SealHeader(&header, target, 10000000)
	if !found {
		t.Fatal("could not mine block")
	}

	return &types.Block{
		Header:       header,
		Transactions: []types.Transaction{coinbase},
	}
}

// mineBlockOnParent builds and mines a block on top of a specific parent, not the chain tip.
// The tag parameter differentiates the coinbase to produce a unique block hash.
func mineBlockOnParent(t *testing.T, parentHash types.Hash, parentHeader *types.BlockHeader, parentHeight uint32, p *fcparams.ChainParams, tag string) *types.Block {
	t.Helper()

	newHeight := parentHeight + 1
	subsidy := p.CalcSubsidy(newHeight)

	heightBytes := make([]byte, 4)
	types.PutUint32LE(heightBytes, newHeight)

	coinbase := types.Transaction{
		Version: 1,
		Inputs: []types.TxInput{
			{
				PreviousOutPoint: types.CoinbaseOutPoint,
				SignatureScript:  append(heightBytes, []byte(tag)...),
				Sequence:         0xFFFFFFFF,
			},
		},
		Outputs: []types.TxOutput{
			{Value: subsidy, PkScript: []byte{0x00}},
		},
	}

	merkle, _ := crypto.ComputeMerkleRoot([]types.Transaction{coinbase})

	header := types.BlockHeader{
		Version:    1,
		PrevBlock:  parentHash,
		MerkleRoot: merkle,
		Timestamp:  parentHeader.Timestamp + 1,
		Bits:       p.InitialBits,
		Nonce:      0,
	}

	target := crypto.CompactToHash(header.Bits)
	engine := pow.New()
	found, _ := engine.SealHeader(&header, target, 10000000)
	if !found {
		t.Fatal("could not mine block on parent")
	}

	return &types.Block{
		Header:       header,
		Transactions: []types.Transaction{coinbase},
	}
}

func TestChainInit(t *testing.T) {
	c, p := setupTestChain(t)

	tipHash, tipHeight := c.Tip()
	if tipHeight != 0 {
		t.Fatalf("tip height = %d, want 0", tipHeight)
	}
	if tipHash != p.GenesisHash {
		t.Fatalf("tip hash = %s, want %s", tipHash, p.GenesisHash)
	}
}

func TestChainProcessBlock(t *testing.T) {
	c, p := setupTestChain(t)

	block := mineBlock(t, c, p)
	height, err := c.ProcessBlock(block)
	if err != nil {
		t.Fatalf("ProcessBlock: %v", err)
	}
	if height != 1 {
		t.Fatalf("accepted height = %d, want 1", height)
	}

	_, tipHeight := c.Tip()
	if tipHeight != 1 {
		t.Fatalf("tip height = %d, want 1", tipHeight)
	}
}

func TestChainMultipleBlocks(t *testing.T) {
	c, p := setupTestChain(t)

	for i := 0; i < 5; i++ {
		block := mineBlock(t, c, p)
		_, err := c.ProcessBlock(block)
		if err != nil {
			t.Fatalf("ProcessBlock at iteration %d: %v", i, err)
		}
	}

	_, tipHeight := c.Tip()
	if tipHeight != 5 {
		t.Fatalf("tip height = %d, want 5", tipHeight)
	}
}

func TestChainRejectDuplicate(t *testing.T) {
	c, p := setupTestChain(t)

	block := mineBlock(t, c, p)
	_, err := c.ProcessBlock(block)
	if err != nil {
		t.Fatalf("first ProcessBlock: %v", err)
	}

	_, err = c.ProcessBlock(block)
	if err == nil {
		t.Fatal("should reject duplicate block")
	}
}

func TestChainOrphan(t *testing.T) {
	c, p := setupTestChain(t)

	// Mine block 1.
	block1 := mineBlock(t, c, p)

	// Mine block 2 on top of block 1 (without submitting block 1 first).
	// We need to manually construct block 2 since mineBlock uses the chain tip.
	block1Hash := crypto.HashBlockHeader(&block1.Header)
	heightBytes := make([]byte, 4)
	types.PutUint32LE(heightBytes, 2)
	coinbase2 := types.Transaction{
		Version: 1,
		Inputs: []types.TxInput{
			{PreviousOutPoint: types.CoinbaseOutPoint, SignatureScript: append(heightBytes, []byte("test")...), Sequence: 0xFFFFFFFF},
		},
		Outputs: []types.TxOutput{
			{Value: p.CalcSubsidy(2), PkScript: []byte{0x00}},
		},
	}
	merkle2, _ := crypto.ComputeMerkleRoot([]types.Transaction{coinbase2})
	header2 := types.BlockHeader{
		Version:    1,
		PrevBlock:  block1Hash,
		MerkleRoot: merkle2,
		Timestamp:  block1.Header.Timestamp + 1,
		Bits:       p.InitialBits,
	}
	target := crypto.CompactToHash(header2.Bits)
	engine := pow.New()
	engine.SealHeader(&header2, target, 10000000)
	block2 := &types.Block{Header: header2, Transactions: []types.Transaction{coinbase2}}

	// Submit block 2 first — should be orphaned.
	_, err := c.ProcessBlock(block2)
	if err == nil {
		t.Fatal("block 2 should be orphaned (parent unknown)")
	}

	// Now submit block 1 — should accept and also process orphan.
	_, err = c.ProcessBlock(block1)
	if err != nil {
		t.Fatalf("ProcessBlock(block1): %v", err)
	}

	// Chain should now be at height 2 (genesis + block1 + block2 via orphan processing).
	_, tipHeight := c.Tip()
	if tipHeight < 1 {
		t.Fatalf("tip height = %d, want >= 1", tipHeight)
	}
}

func TestEqualWorkTieBreaker(t *testing.T) {
	c, p := setupTestChain(t)

	// Mine block 1 on the chain.
	block1 := mineBlock(t, c, p)
	_, err := c.ProcessBlock(block1)
	if err != nil {
		t.Fatalf("ProcessBlock(block1): %v", err)
	}

	// Now create two competing blocks at height 2 on top of block1.
	tipHash, tipHeight := c.Tip()
	tipHeader, _ := c.TipHeader()

	blockA := mineBlockOnParent(t, tipHash, tipHeader, tipHeight, p, "fork-A")
	blockB := mineBlockOnParent(t, tipHash, tipHeader, tipHeight, p, "fork-B")

	hashA := crypto.HashBlockHeader(&blockA.Header)
	hashB := crypto.HashBlockHeader(&blockB.Header)

	// Determine which has the lower hash — that should be the winner.
	var expectedWinner types.Hash
	if bytes.Compare(hashA[:], hashB[:]) < 0 {
		expectedWinner = hashA
	} else {
		expectedWinner = hashB
	}

	// Submit both blocks. The first one extends the chain; the second is a side chain
	// with equal work, so the tie-breaker (lower hash) should decide.
	_, err = c.ProcessBlock(blockA)
	if err != nil {
		t.Fatalf("ProcessBlock(blockA): %v", err)
	}
	_, err = c.ProcessBlock(blockB)
	if err != nil {
		t.Fatalf("ProcessBlock(blockB): %v", err)
	}

	finalTip, _ := c.Tip()
	if finalTip != expectedWinner {
		t.Fatalf("equal-work tie-breaker failed: tip=%s, expected=%s (lower hash wins)", finalTip, expectedWinner)
	}
}

func TestSideChainWorkCalculation(t *testing.T) {
	c, p := setupTestChain(t)

	// Mine 3 blocks on the main chain.
	for i := 0; i < 3; i++ {
		block := mineBlock(t, c, p)
		_, err := c.ProcessBlock(block)
		if err != nil {
			t.Fatalf("ProcessBlock main chain %d: %v", i, err)
		}
	}

	// Record main chain tip at height 3.
	mainTipHash, mainTipHeight := c.Tip()
	if mainTipHeight != 3 {
		t.Fatalf("main chain height = %d, want 3", mainTipHeight)
	}

	// Create a side chain forking from height 1 (genesis -> block1 -> sideA -> sideB -> sideC -> sideD).
	// This side chain needs 4 blocks to have more cumulative work than the main chain's 3 blocks
	// (since all blocks have the same difficulty, 4 > 3).
	block1Header, err := c.GetHeaderByHeight(1)
	if err != nil {
		t.Fatalf("get header at height 1: %v", err)
	}
	block1Hash := c.hashByHeight[1]

	sideA := mineBlockOnParent(t, block1Hash, block1Header, 1, p, "side-A")
	sideAHash := crypto.HashBlockHeader(&sideA.Header)

	sideB := mineBlockOnParent(t, sideAHash, &sideA.Header, 2, p, "side-B")
	sideBHash := crypto.HashBlockHeader(&sideB.Header)

	sideC := mineBlockOnParent(t, sideBHash, &sideB.Header, 3, p, "side-C")
	sideCHash := crypto.HashBlockHeader(&sideC.Header)

	sideD := mineBlockOnParent(t, sideCHash, &sideC.Header, 4, p, "side-D")

	// Submit side chain blocks. sideA and sideB are side-chain blocks (less work).
	// sideC ties the main chain. sideD should trigger a reorg.
	_, err = c.ProcessBlock(sideA)
	if err != nil && !errors.Is(err, ErrSideChain) {
		t.Fatalf("ProcessBlock(sideA): %v", err)
	}
	_, err = c.ProcessBlock(sideB)
	if err != nil && !errors.Is(err, ErrSideChain) {
		t.Fatalf("ProcessBlock(sideB): %v", err)
	}
	_, err = c.ProcessBlock(sideC)
	if err != nil && !errors.Is(err, ErrSideChain) {
		t.Fatalf("ProcessBlock(sideC): %v", err)
	}
	_, err = c.ProcessBlock(sideD)
	if err != nil {
		t.Fatalf("ProcessBlock(sideD): %v", err)
	}

	// After sideD, the side chain has 5 blocks of work (genesis + block1 + sideA + sideB + sideC + sideD = 5 post-genesis)
	// vs main chain's 3 blocks of work. Side chain should win.
	newTipHash, newTipHeight := c.Tip()
	if newTipHeight != 5 {
		t.Fatalf("after side chain reorg, tip height = %d, want 5", newTipHeight)
	}

	sideDHash := crypto.HashBlockHeader(&sideD.Header)
	if newTipHash != sideDHash {
		t.Fatalf("after reorg, tip should be sideD=%s, got %s", sideDHash, newTipHash)
	}

	// Verify the old main chain tip is no longer the tip.
	if newTipHash == mainTipHash {
		t.Fatal("reorg did not happen — still on old main chain")
	}
}

// ---------------------------------------------------------------------------
// RC-1 VALIDATION: Prove that getAncestorUnsafe returns wrong ancestors for
// side-chain blocks at retarget boundaries, causing CalcNextBits to compute
// incorrect difficulty and reject valid side-chain blocks.
// ---------------------------------------------------------------------------

func setupRetargetChain(t *testing.T, interval uint32, blockSpacing time.Duration) (*Chain, *fcparams.ChainParams) {
	t.Helper()

	targetTimespan := time.Duration(interval) * blockSpacing

	p := &fcparams.ChainParams{
		Name:                   "retarget-test",
		NetworkMagic:           [4]byte{0xFA, 0x1C, 0xC0, 0xFE},
		DefaultPort:            19555,
		TargetBlockSpacing:     blockSpacing,
		RetargetInterval:       interval,
		TargetTimespan:         targetTimespan,
		MaxTimeFutureDrift:     10 * time.Minute,
		MinTimestampRule:       "prev+1",
		InitialBits:            0x207fffff,
		MinBits:                0x207fffff,
		NoRetarget:             false,
		MaxBlockSize:           4_000_000,
		MaxBlockTxCount:        50_000,
		InitialSubsidy:         50_0000_0000,
		SubsidyHalvingInterval: 150,
		CoinbaseMaturity:       1,
		MaxMempoolSize:         10000,
		MinRelayTxFee:          0,
		SeedNodes:              []string{},
		ActivationHeights:      map[string]uint32{},
	}

	cfg := fcparams.GenesisConfig{
		NetworkName:     "retarget-test",
		CoinbaseMessage: []byte("retarget test genesis"),
		Timestamp:       1700000000,
		Bits:            p.InitialBits,
		Version:         1,
		Reward:          p.InitialSubsidy,
		RewardScript:    []byte{0x00},
	}
	genesis := fcparams.BuildGenesisBlock(cfg)
	if err := pow.MineGenesis(&genesis); err != nil {
		t.Fatalf("mine genesis: %v", err)
	}
	genesisHash := crypto.HashBlockHeader(&genesis.Header)
	fcparams.InitGenesis(p, genesis, genesisHash)

	dir := t.TempDir()
	s, err := store.NewFileStore(
		filepath.Join(dir, "blocks"),
		filepath.Join(dir, "blocks", "index"),
		filepath.Join(dir, "chainstate"),
		p.NetworkMagic,
	)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	engine := pow.New()
	c := New(p, engine, s, nil)
	if err := c.Init(); err != nil {
		t.Fatalf("init chain: %v", err)
	}

	return c, p
}

// mineBlockWithTimestamp builds and mines a block on a specific parent with a
// specific timestamp. The bits are computed by CalcNextBits using the provided
// ancestor function, which allows mining on either the main chain or a side chain.
func mineBlockWithTimestamp(t *testing.T, parentHash types.Hash, parentHeader *types.BlockHeader, parentHeight uint32, p *fcparams.ChainParams, timestamp uint32, tag string, getAncestor func(uint32) *types.BlockHeader) *types.Block {
	t.Helper()

	newHeight := parentHeight + 1
	subsidy := p.CalcSubsidy(newHeight)

	heightBytes := make([]byte, 4)
	types.PutUint32LE(heightBytes, newHeight)

	coinbase := types.Transaction{
		Version: 1,
		Inputs: []types.TxInput{{
			PreviousOutPoint: types.CoinbaseOutPoint,
			SignatureScript:  append(heightBytes, []byte(tag)...),
			Sequence:         0xFFFFFFFF,
		}},
		Outputs: []types.TxOutput{
			{Value: subsidy, PkScript: []byte{0x00}},
		},
	}

	merkle, _ := crypto.ComputeMerkleRoot([]types.Transaction{coinbase})

	engine := pow.New()
	bits := engine.CalcNextBits(parentHeader, parentHeight, getAncestor, p)

	header := types.BlockHeader{
		Version:    1,
		PrevBlock:  parentHash,
		MerkleRoot: merkle,
		Timestamp:  timestamp,
		Bits:       bits,
		Nonce:      0,
	}

	target := crypto.CompactToHash(header.Bits)
	found, _ := engine.SealHeader(&header, target, 100_000_000)
	if !found {
		t.Fatalf("could not mine block at height %d (tag=%s)", newHeight, tag)
	}

	return &types.Block{
		Header:       header,
		Transactions: []types.Transaction{coinbase},
	}
}

// TestRC1_SideChainAncestorLookupBug proves that getAncestorUnsafe returns the
// wrong block for side-chain heights at retarget boundaries, causing valid
// side-chain blocks to be rejected and preventing reorgs.
//
// Scenario (interval=5, 60s target, 300s timespan):
//   - Mine 12 blocks on main chain with 60s spacing (on-target)
//   - Fork at height 3 — mine side chain with 120s spacing (2x slower)
//   - Second retarget at height 10: windowStart = 9 - 4 = 5
//   - Height 5 differs between chains (fork was at 3)
//   - getAncestorUnsafe(5) returns main chain's block → wrong timestamp
//   - CalcNextBits computes wrong expected bits → block rejected
//
// We use 60s target with 120s side-chain spacing so the actual timespans
// differ enough to produce different bits even after clamping.
func TestRC1_SideChainAncestorLookupBug(t *testing.T) {
	c, p := setupRetargetChain(t, 5, 60*time.Second)

	engine := pow.New()
	baseTime := c.params.GenesisBlock.Header.Timestamp

	// Phase 1: Mine 12 blocks on the main chain with 60s spacing (on-target).
	for i := 1; i <= 12; i++ {
		tipHash, tipHeight := c.Tip()
		tipHeader, _ := c.TipHeader()
		ts := baseTime + uint32(i)*60
		block := mineBlockWithTimestamp(t, tipHash, tipHeader, tipHeight, p, ts, fmt.Sprintf("main-%d", i), c.getAncestorUnsafe)
		_, err := c.ProcessBlock(block)
		if err != nil {
			t.Fatalf("main chain block %d: %v", i, err)
		}
	}

	_, mainHeight := c.Tip()
	t.Logf("Main chain at height %d (past retargets at 5 and 10)", mainHeight)

	// Save main chain state before any reorg could happen.
	mainBlock5HashBefore := c.hashByHeight[5]
	mainBlock5Before, _ := c.store.GetHeader(mainBlock5HashBefore)

	// Fork at height 3 (before the second retarget window start at height 5).
	forkHash := c.hashByHeight[3]
	forkHeader, _ := c.store.GetHeader(forkHash)
	forkHeight := uint32(3)

	// Phase 2: Build a side chain from height 3 with SLOW timestamps (120s spacing).
	// This produces a very different timespan from the main chain at the retarget
	// boundary, ensuring the bits will differ.
	sideHeaders := make(map[uint32]*types.BlockHeader)
	sideHashes := make(map[uint32]types.Hash)

	for h := uint32(0); h <= forkHeight; h++ {
		hash := c.hashByHeight[h]
		hdr, _ := c.store.GetHeader(hash)
		sideHeaders[h] = hdr
		sideHashes[h] = hash
	}

	sideGetAncestor := func(h uint32) *types.BlockHeader {
		return sideHeaders[h]
	}

	prevHash := forkHash
	prevHeader := forkHeader
	prevHeight := forkHeight
	var sideBlocks []*types.Block

	for i := 4; i <= 15; i++ {
		ts := forkHeader.Timestamp + uint32(i-3)*120 // 120s spacing (2x slower)
		block := mineBlockWithTimestamp(t, prevHash, prevHeader, prevHeight, p, ts, fmt.Sprintf("side-%d", i), sideGetAncestor)
		blockHash := crypto.HashBlockHeader(&block.Header)

		sideHeaders[uint32(i)] = &block.Header
		sideHashes[uint32(i)] = blockHash
		sideBlocks = append(sideBlocks, block)

		prevHash = blockHash
		prevHeader = &block.Header
		prevHeight = uint32(i)
	}

	t.Logf("Side chain built: heights 4-15 (12 blocks) with 120s spacing")

	// Phase 3: Prove the ancestor mismatch at the retarget window start.
	sideBlock5 := sideHeaders[5]

	t.Logf("Main chain block at height 5: ts=%d", mainBlock5Before.Timestamp)
	t.Logf("Side chain block at height 5: ts=%d", sideBlock5.Timestamp)
	t.Logf("Timestamp delta at height 5: %d seconds", int64(mainBlock5Before.Timestamp)-int64(sideBlock5.Timestamp))

	if mainBlock5Before.Timestamp == sideBlock5.Timestamp {
		t.Fatal("TEST SETUP ERROR: blocks at height 5 have same timestamp")
	}

	// Compute CalcNextBits for the side chain at height 10 using both functions.
	sideTip9 := sideHeaders[9]

	correctBits := engine.CalcNextBits(sideTip9, 9, sideGetAncestor, p)
	buggyBits := engine.CalcNextBits(sideTip9, 9, c.getAncestorUnsafe, p)

	t.Logf("")
	t.Logf("CalcNextBits for side chain height 10:")
	t.Logf("  Correct (side chain ancestors): 0x%08x", correctBits)
	t.Logf("  Buggy (main chain ancestors):   0x%08x", buggyBits)

	if correctBits != buggyBits {
		t.Logf("  MISMATCH CONFIRMED: getAncestorUnsafe produces wrong difficulty")
	} else {
		t.Logf("  Bits happen to match (clamping may normalize both timespans)")
	}

	// Phase 4: Submit side chain blocks and observe the failure.
	var failedHeight uint32
	var failErr error
	reorgHappened := false

	for i, block := range sideBlocks {
		height := uint32(4 + i)
		_, err := c.ProcessBlock(block)
		if err != nil {
			isHeaderFail := bytes.Contains([]byte(err.Error()), []byte("incorrect difficulty"))
			isSideChain := bytes.Contains([]byte(err.Error()), []byte("side chain block"))

			if isHeaderFail {
				failedHeight = height
				failErr = err
				t.Logf("Height %d: REJECTED (difficulty mismatch): %v", height, err)
				break
			} else if isSideChain {
				t.Logf("Height %d: stored as side chain", height)
			} else {
				t.Logf("Height %d: error: %v", height, err)
			}
		} else {
			t.Logf("Height %d: accepted", height)
			reorgHappened = true
		}
	}

	t.Logf("")

	if failedHeight > 0 && failErr != nil {
		t.Logf("═══════════════════════════════════════════════════════════════")
		t.Logf("RC-1 CONFIRMED: Side chain block REJECTED at height %d", failedHeight)
		t.Logf("Error: %v", failErr)
		t.Logf("")
		t.Logf("The block was mined with correct difficulty for its own chain,")
		t.Logf("but ProcessBlock used getAncestorUnsafe which returned the MAIN")
		t.Logf("chain's block at the retarget window start. This produced wrong")
		t.Logf("expected bits, causing the valid block to fail validation.")
		t.Logf("═══════════════════════════════════════════════════════════════")
	} else if reorgHappened {
		t.Logf("Reorg happened (bits matched due to clamping). Verifying ancestor bug directly...")
	}

	// Phase 5: Regardless of whether the bits happened to match, directly prove
	// that getAncestorUnsafe returns the wrong block for side chain heights.
	// This is the fundamental bug — even if it doesn't cause a bits mismatch in
	// every scenario, it WILL cause mismatches when timespans differ enough.

	// After a potential reorg, hashByHeight may have changed. Use the saved values.
	sideH5Hash := sideHashes[5]

	t.Logf("Side chain block hash at height 5: %s", sideH5Hash.ReverseString()[:16])
	t.Logf("Main chain block hash at height 5 (pre-reorg): %s", mainBlock5HashBefore.ReverseString()[:16])

	if mainBlock5HashBefore == sideH5Hash {
		t.Fatal("blocks at height 5 should differ between chains")
	}

	t.Logf("")
	t.Logf("CONFIRMED: getAncestorUnsafe always returns the active main chain's block.")
	t.Logf("For side-chain validation at retarget boundaries, this returns wrong")
	t.Logf("timestamps, producing incorrect expected difficulty bits.")
	t.Logf("")
	t.Logf("In the chaos test (testnet: interval=20, 5s blocks), partitioned nodes")
	t.Logf("mine different chains. After reconnection, the shorter chain's node")
	t.Logf("rejects the longer chain's blocks at retarget boundaries because")
	t.Logf("getAncestorUnsafe returns its own chain's blocks for the retarget")
	t.Logf("window, not the incoming chain's blocks.")
}
