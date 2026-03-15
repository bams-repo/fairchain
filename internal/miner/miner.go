package miner

import (
	"context"
	"fmt"
	"time"

	"github.com/bams-repo/fairchain/internal/chain"
	"github.com/bams-repo/fairchain/internal/consensus"
	"github.com/bams-repo/fairchain/internal/crypto"
	"github.com/bams-repo/fairchain/internal/logging"
	"github.com/bams-repo/fairchain/internal/mempool"
	"github.com/bams-repo/fairchain/internal/params"
	"github.com/bams-repo/fairchain/internal/types"
)

// TimeSource provides network-adjusted time for block timestamp construction.
type TimeSource interface {
	Now() int64
}

type localClock struct{}

func (localClock) Now() int64 { return time.Now().Unix() }

// Miner builds block templates and searches for valid PoW solutions.
type Miner struct {
	chain        *chain.Chain
	engine       consensus.Engine
	mempool      *mempool.Mempool
	params       *params.ChainParams
	rewardScript []byte
	timeSource   TimeSource
	onBlock      func(*types.Block)
}

// New creates a new Miner. ts may be nil, in which case raw local time is used.
func New(c *chain.Chain, e consensus.Engine, mp *mempool.Mempool, p *params.ChainParams, rewardScript []byte, ts TimeSource, onBlock func(*types.Block)) *Miner {
	if ts == nil {
		ts = localClock{}
	}
	return &Miner{
		chain:        c,
		engine:       e,
		mempool:      mp,
		params:       p,
		rewardScript: rewardScript,
		timeSource:   ts,
		onBlock:      onBlock,
	}
}

// Run starts the mining loop. It blocks until ctx is cancelled.
func (m *Miner) Run(ctx context.Context) {
	logging.L.Info("starting mining loop", "component", "miner")
	for {
		select {
		case <-ctx.Done():
			logging.L.Info("stopping mining loop", "component", "miner")
			return
		default:
		}

		block, err := m.MineOne(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			logging.L.Error("mining error", "component", "miner", "error", err)
			time.Sleep(time.Second)
			continue
		}

		if block != nil && m.onBlock != nil {
			m.onBlock(block)
		}

		// Brief yield to prevent CPU spin on very fast mining (regtest).
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// MineOne builds a template and attempts to mine a single block.
// Returns nil if ctx is cancelled before a solution is found.
func (m *Miner) MineOne(ctx context.Context) (*types.Block, error) {
	tipHash, tipHeight := m.chain.Tip()
	tipHeader, err := m.chain.TipHeader()
	if err != nil {
		return nil, fmt.Errorf("get tip header: %w", err)
	}

	newHeight := tipHeight + 1
	subsidy := m.params.CalcSubsidy(newHeight)

	tmpl := m.mempool.BlockTemplate()

	coinbaseTx := m.buildCoinbase(newHeight, subsidy+tmpl.TotalFees)

	txs := make([]types.Transaction, 0, 1+len(tmpl.Transactions))
	txs = append(txs, coinbaseTx)
	for _, tx := range tmpl.Transactions {
		txs = append(txs, *tx)
	}

	merkle, err := crypto.ComputeMerkleRoot(txs)
	if err != nil {
		return nil, fmt.Errorf("compute merkle root: %w", err)
	}

	ts := uint32(m.timeSource.Now())
	if ts <= tipHeader.Timestamp {
		ts = tipHeader.Timestamp + 1
	}

	header := types.BlockHeader{
		Version:    1,
		PrevBlock:  tipHash,
		MerkleRoot: merkle,
		Timestamp:  ts,
		Nonce:      0,
	}

	if err := m.engine.PrepareHeader(&header, tipHeader, tipHeight, m.chain.GetAncestor, m.params); err != nil {
		return nil, fmt.Errorf("prepare header: %w", err)
	}

	target := crypto.CompactToHash(header.Bits)

	const batchSize = 100000
	for {
		select {
		case <-ctx.Done():
			return nil, nil
		default:
		}

		found, err := m.engine.SealHeader(&header, target, batchSize)
		if err != nil {
			return nil, err
		}
		if found {
			block := &types.Block{
				Header:       header,
				Transactions: txs,
			}
			blockHash := crypto.HashBlockHeader(&block.Header)
			logging.L.Info("found block", "component", "miner", "hash", blockHash.ReverseString(), "height", newHeight, "nonce", header.Nonce)
			return block, nil
		}

		if header.Nonce == 0 {
			now := uint32(m.timeSource.Now())
			if now <= tipHeader.Timestamp {
				now = tipHeader.Timestamp + 1
			}
			header.Timestamp = now
			merkle, _ = crypto.ComputeMerkleRoot(txs)
			header.MerkleRoot = merkle
		}
	}
}

func (m *Miner) buildCoinbase(height uint32, subsidy uint64) types.Transaction {
	// BIP34: encode height as a CScript push — [pushLen][height LE bytes][tag].
	heightBytes := make([]byte, 4)
	types.PutUint32LE(heightBytes, height)
	msg := make([]byte, 0, 1+4+len("fairchain"))
	msg = append(msg, 0x04) // push 4 bytes
	msg = append(msg, heightBytes...)
	msg = append(msg, []byte("fairchain")...)

	return types.Transaction{
		Version: 1,
		Inputs: []types.TxInput{
			{
				PreviousOutPoint: types.CoinbaseOutPoint,
				SignatureScript:  msg,
				Sequence:         0xFFFFFFFF,
			},
		},
		Outputs: []types.TxOutput{
			{
				Value:    subsidy,
				PkScript: m.rewardScript,
			},
		},
		LockTime: 0,
	}
}
