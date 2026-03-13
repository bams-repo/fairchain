package miner

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/bams-repo/fairchain/internal/chain"
	"github.com/bams-repo/fairchain/internal/consensus/pow"
	"github.com/bams-repo/fairchain/internal/crypto"
	"github.com/bams-repo/fairchain/internal/mempool"
	fcparams "github.com/bams-repo/fairchain/internal/params"
	"github.com/bams-repo/fairchain/internal/store"
	"github.com/bams-repo/fairchain/internal/types"
)

func setupTestMiner(t *testing.T) (*Miner, *chain.Chain, *fcparams.ChainParams) {
	t.Helper()

	p := &fcparams.ChainParams{}
	*p = *fcparams.Regtest

	cfg := fcparams.GenesisConfig{
		NetworkName:     "regtest",
		CoinbaseMessage: []byte("miner test genesis"),
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
	c := chain.New(p, engine, s, nil)
	if err := c.Init(); err != nil {
		t.Fatalf("init chain: %v", err)
	}

	mp := mempool.New(p, c.UtxoSet())
	m := New(c, engine, mp, p, []byte{0x00}, nil)

	return m, c, p
}

func TestNonceWrapTimestamp(t *testing.T) {
	m, c, _ := setupTestMiner(t)

	// Mine a block so we have a tip with a known timestamp.
	ctx := context.Background()
	block, err := m.MineOne(ctx)
	if err != nil {
		t.Fatalf("MineOne: %v", err)
	}
	if block == nil {
		t.Fatal("MineOne returned nil block")
	}

	_, err = c.ProcessBlock(block)
	if err != nil {
		t.Fatalf("ProcessBlock: %v", err)
	}

	// Now verify that the nonce-wrap logic in MineOne produces a valid timestamp.
	// We can't easily trigger a nonce wrap in a test (regtest difficulty is very easy),
	// but we can verify the timestamp logic directly by checking that the miner's
	// output always has timestamp > parent timestamp.
	tipHeader, _ := c.TipHeader()

	for i := 0; i < 5; i++ {
		block, err = m.MineOne(ctx)
		if err != nil {
			t.Fatalf("MineOne iteration %d: %v", i, err)
		}
		if block == nil {
			t.Fatalf("MineOne iteration %d returned nil", i)
		}

		if block.Header.Timestamp <= tipHeader.Timestamp {
			t.Fatalf("block timestamp %d <= parent timestamp %d (iteration %d)",
				block.Header.Timestamp, tipHeader.Timestamp, i)
		}

		_, err = c.ProcessBlock(block)
		if err != nil {
			t.Fatalf("ProcessBlock iteration %d: %v", i, err)
		}
		tipHeader, _ = c.TipHeader()
	}
}

func TestMineOneProducesValidBlock(t *testing.T) {
	m, c, p := setupTestMiner(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	block, err := m.MineOne(ctx)
	if err != nil {
		t.Fatalf("MineOne: %v", err)
	}
	if block == nil {
		t.Fatal("MineOne returned nil")
	}

	hash := crypto.HashBlockHeader(&block.Header)
	target := crypto.CompactToHash(block.Header.Bits)
	if !hash.LessOrEqual(target) {
		t.Fatal("mined block does not meet PoW target")
	}

	if len(block.Transactions) < 1 {
		t.Fatal("block has no transactions")
	}

	if block.Transactions[0].Inputs[0].PreviousOutPoint != types.CoinbaseOutPoint {
		t.Fatal("first transaction is not a coinbase")
	}

	height, err := c.ProcessBlock(block)
	if err != nil {
		t.Fatalf("ProcessBlock: %v", err)
	}
	if height != 1 {
		t.Fatalf("accepted at height %d, want 1", height)
	}

	_ = p
}
