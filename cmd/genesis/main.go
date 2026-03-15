package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/bams-repo/fairchain/internal/consensus/pow"
	"github.com/bams-repo/fairchain/internal/crypto"
	fcparams "github.com/bams-repo/fairchain/internal/params"
	"github.com/bams-repo/fairchain/internal/types"
)

func main() {
	network := flag.String("network", "regtest", "Network name: mainnet, testnet, regtest")
	message := flag.String("message", "fairchain genesis", "Coinbase message for genesis block")
	timestamp := flag.Int64("timestamp", 0, "Unix timestamp (0 = now)")
	flag.Parse()

	p := fcparams.NetworkByName(*network)
	if p == nil {
		fmt.Fprintf(os.Stderr, "unknown network: %s\n", *network)
		os.Exit(1)
	}

	ts := uint32(time.Now().Unix())
	if *timestamp > 0 {
		ts = uint32(*timestamp)
	}

	cfg := fcparams.GenesisConfig{
		NetworkName:     p.Name,
		CoinbaseMessage: []byte(*message),
		Timestamp:       ts,
		Bits:            p.InitialBits,
		Version:         1,
		Reward:          p.InitialSubsidy,
		RewardScript:    []byte{0x00},
	}

	if p.Name == "testnet" {
		cfg.ExtraOutputs = []types.TxOutput{
			{
				Value:    fcparams.TestnetPremineAmount,
				PkScript: fcparams.TestnetBurnScript,
			},
		}
	}

	log.Printf("Building genesis block for %s...", p.Name)
	log.Printf("  Bits:      0x%08x", cfg.Bits)
	log.Printf("  Timestamp: %d", cfg.Timestamp)
	log.Printf("  Message:   %q", string(cfg.CoinbaseMessage))
	if len(cfg.ExtraOutputs) > 0 {
		log.Printf("  Extra outputs: %d", len(cfg.ExtraOutputs))
	}

	block := fcparams.BuildGenesisBlock(cfg)

	log.Println("Mining genesis block (this may take a moment)...")
	start := time.Now()
	if err := pow.MineGenesis(&block); err != nil {
		log.Fatalf("Failed to mine genesis: %v", err)
	}
	elapsed := time.Since(start)

	hash := crypto.HashBlockHeader(&block.Header)

	log.Printf("Genesis block mined in %v", elapsed)
	log.Printf("  Hash:       %s", hash.ReverseString())
	log.Printf("  Nonce:      %d", block.Header.Nonce)
	log.Printf("  MerkleRoot: %s", block.Header.MerkleRoot.ReverseString())
	log.Printf("  Timestamp:  %d", block.Header.Timestamp)

	fmt.Println("\n// --- Genesis block Go definition ---")
	fmt.Printf("// Network: %s\n", p.Name)
	fmt.Printf("// Hash:    %s\n", hash.ReverseString())
	fmt.Printf("// Nonce:   %d\n", block.Header.Nonce)
	fmt.Printf("// Mined:   %s\n", time.Unix(int64(block.Header.Timestamp), 0).UTC())
	fmt.Printf("// Elapsed: %v\n", elapsed)
	fmt.Println("//")
	fmt.Printf("// Bits:       0x%08x\n", block.Header.Bits)
	fmt.Printf("// MerkleRoot: %s\n", block.Header.MerkleRoot)
	fmt.Printf("// Timestamp:  %d\n", block.Header.Timestamp)
}
