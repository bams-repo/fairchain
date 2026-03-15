package main

import (
	"context"
	"flag"
	"fmt"
	"math/big"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/bams-repo/fairchain/internal/chain"
	"github.com/bams-repo/fairchain/internal/config"
	"github.com/bams-repo/fairchain/internal/consensus/pow"
	"github.com/bams-repo/fairchain/internal/crypto"
	"github.com/bams-repo/fairchain/internal/logging"
	"github.com/bams-repo/fairchain/internal/mempool"
	"github.com/bams-repo/fairchain/internal/metrics"
	"github.com/bams-repo/fairchain/internal/miner"
	"github.com/bams-repo/fairchain/internal/p2p"
	fcparams "github.com/bams-repo/fairchain/internal/params"
	"github.com/bams-repo/fairchain/internal/rpc"
	"github.com/bams-repo/fairchain/internal/store"
	"github.com/bams-repo/fairchain/internal/timeadjust"
	"github.com/bams-repo/fairchain/internal/types"
	"github.com/bams-repo/fairchain/internal/version"
	"github.com/bams-repo/fairchain/internal/wallet"
)

func main() {
	configPath := flag.String("config", "", "Path to config file (JSON)")
	confPath := flag.String("conf", "", "Path to fairchain.conf (INI-style)")
	network := flag.String("network", "", "Override network (mainnet/testnet/regtest)")
	dataDir := flag.String("datadir", "", "Override data directory")
	listen := flag.String("listen", "", "Override P2P listen address (host:port)")
	rpcBind := flag.String("rpcbind", "", "RPC bind address (default: 127.0.0.1)")
	rpcPort := flag.String("rpcport", "", "RPC port (default: network-dependent)")
	rpcAddr := flag.String("rpc", "", "Override full RPC address (host:port) — legacy flag")
	mine := flag.Bool("mine", false, "Enable mining")
	addNode := flag.String("addnode", "", "Add a peer to connect to (ip:port)")
	seedPeers := flag.String("seed-peers", "", "Comma-separated seed peer addresses (ip:port,ip:port)")
	connectPeers := flag.String("connect", "", "Connect ONLY to these peers (ip:port,ip:port) — disables all discovery")
	noSeedNodes := flag.Bool("noseednode", false, "Suppress hardcoded seed nodes from chain params")
	logLevel := flag.String("log-level", "info", "Log level: debug, info, warn, error")
	debugFlag := flag.Bool("debug", false, "Enable hyper-verbose debug output (block relay, peer topology, sync state)")
	noRPCAuth := flag.Bool("norpcauth", false, "Disable RPC authentication (testing/regtest only)")
	migrateFlag := flag.Bool("migrate", false, "Migrate legacy blocks.db to new format")
	printVersion := flag.Bool("version", false, "Print version and exit")
	flag.Parse()

	if *printVersion {
		fmt.Printf("Fairchain Daemon version v%s\n", version.String())
		os.Exit(0)
	}

	logging.Init(*logLevel)
	if *debugFlag {
		logging.EnableDebug()
	}
	log := logging.L

	// Load config: try fairchain.conf first, then JSON, then defaults.
	var cfg *config.Config
	var err error

	// Determine network early for conf file section matching.
	earlyNetwork := *network
	if earlyNetwork == "" {
		earlyNetwork = "regtest"
	}

	if *confPath != "" {
		cfg, err = config.LoadConf(*confPath, earlyNetwork)
		if err != nil {
			log.Error("failed to load conf", "error", err)
			os.Exit(1)
		}
	} else if *configPath != "" {
		cfg, err = config.LoadConfig(*configPath)
		if err != nil {
			log.Error("failed to load config", "error", err)
			os.Exit(1)
		}
	} else {
		cfg = config.DefaultConfig()
		// Try loading fairchain.conf from default data dir if it exists.
		defaultConf := cfg.ConfFilePath()
		if _, statErr := os.Stat(defaultConf); statErr == nil {
			cfg, err = config.LoadConf(defaultConf, earlyNetwork)
			if err != nil {
				log.Warn("failed to load default fairchain.conf, using defaults", "error", err)
				cfg = config.DefaultConfig()
			}
		}
	}

	// Apply CLI overrides.
	if *network != "" {
		cfg.Network = *network
	}
	if *dataDir != "" {
		cfg.DataDir = *dataDir
	}
	if *listen != "" {
		cfg.ListenAddr = *listen
	}
	// RPC address: -rpc (legacy full addr) or -rpcbind/-rpcport (Bitcoin Core style).
	if *rpcAddr != "" {
		cfg.RPCAddr = *rpcAddr
	} else if *rpcBind != "" || *rpcPort != "" {
		host, port := "127.0.0.1", "19445"
		if existing := cfg.RPCAddr; existing != "" {
			if h, p, err := splitHostPort(existing); err == nil {
				host, port = h, p
			}
		}
		if *rpcBind != "" {
			host = *rpcBind
		}
		if *rpcPort != "" {
			port = *rpcPort
		}
		cfg.RPCAddr = host + ":" + port
	}
	if *mine {
		cfg.MiningEnabled = true
	}
	if *addNode != "" {
		cfg.SeedPeers = append(cfg.SeedPeers, *addNode)
	}
	if *seedPeers != "" {
		cfg.SeedPeers = append(cfg.SeedPeers, strings.Split(*seedPeers, ",")...)
	}

	// Resolve chain params.
	params := fcparams.NetworkByName(cfg.Network)
	if params == nil {
		log.Error("unknown network", "network", cfg.Network)
		os.Exit(1)
	}
	cfg.DataDirName = params.DataDirName

	// Mine and set genesis for the network.
	initNetworkGenesis(params)

	// Ensure data directory tree exists.
	if err := cfg.EnsureDataDir(); err != nil {
		log.Error("failed to create data dir", "error", err)
		os.Exit(1)
	}

	log.Info("starting fairchain node",
		"network", cfg.Network,
		"datadir", cfg.NetworkDataDir(),
		"blocks", cfg.BlocksDir(),
		"chainstate", cfg.ChainstateDir())

	// Handle migration from legacy format.
	if *migrateFlag {
		if err := migrateFromLegacy(cfg, params); err != nil {
			log.Error("migration failed", "error", err)
			os.Exit(1)
		}
		log.Info("migration complete")
		os.Exit(0)
	}

	// Check for legacy data and warn.
	legacyPath := cfg.LegacyDBPath()
	if _, statErr := os.Stat(legacyPath); statErr == nil {
		log.Warn("legacy blocks.db detected — run with --migrate to convert", "path", legacyPath)
	}
	// Also check the root datadir for legacy blocks.db.
	rootLegacy := cfg.DBPath()
	if rootLegacy != legacyPath {
		if _, statErr := os.Stat(rootLegacy); statErr == nil {
			log.Warn("legacy blocks.db detected at root — run with --migrate to convert", "path", rootLegacy)
		}
	}

	// Acquire lock file.
	lockFile, err := store.AcquireLock(cfg.LockFilePath())
	if err != nil {
		log.Error("failed to acquire lock file — is another instance running?", "error", err)
		os.Exit(1)
	}
	defer store.ReleaseLock(lockFile)

	// Open block store (flat files + LevelDB).
	blockStore, err := store.NewFileStore(
		cfg.BlocksDir(),
		cfg.BlockIndexDir(),
		cfg.ChainstateDir(),
		params.NetworkMagic,
	)
	if err != nil {
		log.Error("failed to open block store", "error", err)
		os.Exit(1)
	}

	// Open peer store (bbolt).
	peerStore, err := store.NewBoltStore(cfg.PeerDBPath())
	if err != nil {
		blockStore.Close()
		log.Error("failed to open peer store", "error", err)
		os.Exit(1)
	}

	// Create consensus engine.
	engine := pow.New()

	// Network-adjusted clock (Bitcoin Core GetAdjustedTime parity).
	adjClock := timeadjust.New()

	// Create blockchain.
	bc := chain.New(params, engine, blockStore, adjClock)
	if err := bc.Init(); err != nil {
		peerStore.Close()
		blockStore.Close()
		log.Error("failed to initialize chain", "error", err)
		os.Exit(1)
	}

	tipHash, tipHeight := bc.Tip()
	log.Info("chain initialized", "tip", tipHash.ReverseString(), "height", tipHeight)

	// Create mempool with UTXO-aware validation.
	mp := mempool.New(params, bc.UtxoSet())
	mp.SetTipHeight(tipHeight)

	// Load persisted mempool if available.
	if mempoolData, loadErr := os.ReadFile(cfg.MempoolPath()); loadErr == nil && len(mempoolData) > 0 {
		loaded := mp.LoadFromBytes(mempoolData)
		log.Info("loaded mempool from disk", "transactions", loaded)
	}

	// Context for graceful shutdown.
	ctx, cancel := context.WithCancel(context.Background())

	// Build P2P options (Bitcoin Core parity: -connect, -noseednode).
	p2pOpts := &p2p.ManagerOptions{
		NoSeedNodes: *noSeedNodes,
	}
	if *connectPeers != "" {
		p2pOpts.ConnectOnly = strings.Split(*connectPeers, ",")
	}

	// Start P2P manager.
	p2pMgr := p2p.NewManager(params, bc, mp, peerStore, cfg.ListenAddr, cfg.MaxInbound, cfg.MaxOutbound, cfg.SeedPeers, adjClock, p2pOpts)

	// Load peers.dat and merge into peer store.
	store.LoadPeersDat(cfg.PeersDatPath(), peerStore)

	if err := p2pMgr.Start(ctx); err != nil {
		cancel()
		peerStore.Close()
		blockStore.Close()
		log.Error("failed to start P2P", "error", err)
		os.Exit(1)
	}

	// Initialize HD wallet.
	hdWallet, err := wallet.NewHDWallet(cfg.WalletDir(), params.AddressPrefix)
	if err != nil {
		cancel()
		p2pMgr.Stop()
		peerStore.Close()
		blockStore.Close()
		log.Error("failed to initialize wallet", "error", err)
		os.Exit(1)
	}
	defaultAddr := hdWallet.GetDefaultAddress()
	log.Info("wallet loaded",
		"address", defaultAddr,
		"keys", hdWallet.KeyCount())

	// Start RPC server with authentication.
	var rpcAuth *rpc.AuthConfig
	if !*noRPCAuth {
		rpcAuth = &rpc.AuthConfig{
			User:       cfg.RPCUser,
			Password:   cfg.RPCPassword,
			CookiePath: cfg.RPCCookiePath(),
		}
	}
	rpcServer, err := rpc.New(cfg.RPCAddr, bc, mp, p2pMgr, params, rpcAuth)
	if err != nil {
		cancel()
		logging.L.Error("failed to create RPC server", "error", err)
		os.Exit(1)
	}
	rpcServer.SetWallet(hdWallet)
	rpcServer.SetBroadcastTx(p2pMgr.BroadcastTx)
	if err := rpcServer.Start(); err != nil {
		cancel()
		p2pMgr.Stop()
		peerStore.Close()
		blockStore.Close()
		log.Error("failed to start RPC", "error", err)
		os.Exit(1)
	}

	// Start miner if enabled.
	if cfg.MiningEnabled {
		rewardScript := hdWallet.GetDefaultP2PKHScript()
		if rewardScript == nil {
			cancel()
			p2pMgr.Stop()
			rpcServer.Stop(context.Background())
			peerStore.Close()
			blockStore.Close()
			log.Error("wallet has no keys for mining reward")
			os.Exit(1)
		}
		_, pkh := hdWallet.MiningKeyCompat()
		log.Info("mining key loaded",
			"pubkey_hash", fmt.Sprintf("%x", pkh[:]),
			"script_len", len(rewardScript))
		m := miner.New(bc, engine, mp, params, rewardScript, adjClock, func(block *types.Block) {
			height, err := bc.ProcessBlock(block)
			if err != nil {
				log.Warn("mined block rejected", "error", err)
				return
			}
			var confirmedHashes []types.Hash
			for _, tx := range block.Transactions {
				txHash, hashErr := crypto.HashTransaction(&tx)
				if hashErr == nil {
					confirmedHashes = append(confirmedHashes, txHash)
				}
			}
			mp.RemoveTxs(confirmedHashes)
			mp.SetTipHeight(height)
			blockHash := crypto.HashBlockHeader(&block.Header)
			metrics.Global.BlocksMined.Add(1)
			log.Info("mined block accepted", "hash", blockHash.ReverseString(), "height", height)
			p2pMgr.BroadcastBlock(blockHash, block)
		})
		go m.Run(ctx)
	}

	// Wire up the "stop" RPC to trigger graceful shutdown.
	sigCh := make(chan os.Signal, 1)
	rpcServer.SetShutdownFunc(func() {
		sigCh <- syscall.SIGTERM
	})

	// Wait for shutdown signal.
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	log.Info("received shutdown signal", "signal", sig)

	cancel()

	log.Info("stopping RPC server...")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := rpcServer.Stop(shutdownCtx); err != nil {
		log.Warn("RPC shutdown error", "error", err)
	}

	log.Info("stopping P2P manager...")
	p2pMgr.Stop()

	// Persist mempool to disk.
	if data := mp.DumpToBytes(); len(data) > 0 {
		if err := os.WriteFile(cfg.MempoolPath(), data, 0600); err != nil {
			log.Warn("failed to persist mempool", "error", err)
		} else {
			log.Info("mempool persisted", "transactions", mp.Count())
		}
	}

	// Dump peers.dat.
	store.DumpPeersDat(cfg.PeersDatPath(), peerStore)

	log.Info("closing peer store...")
	if err := peerStore.Close(); err != nil {
		log.Warn("peer store close error", "error", err)
	}

	log.Info("closing block store...")
	if err := blockStore.Close(); err != nil {
		log.Warn("block store close error", "error", err)
	}

	log.Info("shutdown complete")
}

func initNetworkGenesis(p *fcparams.ChainParams) {
	if !p.GenesisHash.IsZero() {
		computed := crypto.HashBlockHeader(&p.GenesisBlock.Header)
		if computed != p.GenesisHash {
			logging.L.Error("genesis hash verification failed",
				"network", p.Name,
				"expected", p.GenesisHash.ReverseString(),
				"computed", computed.ReverseString())
			os.Exit(1)
		}
		return
	}

	if p.Name == "mainnet" {
		logging.L.Error("mainnet requires a hardcoded genesis block in params")
		os.Exit(1)
	}

	cfg := fcparams.GenesisConfig{
		NetworkName:     p.Name,
		CoinbaseMessage: []byte(fmt.Sprintf("fairchain %s genesis", p.Name)),
		Timestamp:       1773212462,
		Bits:            p.InitialBits,
		Version:         1,
		Reward:          p.InitialSubsidy,
		RewardScript:    []byte{0x00},
	}

	block := fcparams.BuildGenesisBlock(cfg)
	if err := pow.MineGenesis(&block); err != nil {
		logging.L.Error("failed to mine genesis", "error", err)
		os.Exit(1)
	}

	hash := crypto.HashBlockHeader(&block.Header)
	fcparams.InitGenesis(p, block, hash)
	logging.L.Info("genesis block", "hash", hash.ReverseString(), "nonce", block.Header.Nonce)
}

func splitHostPort(addr string) (string, string, error) {
	idx := strings.LastIndex(addr, ":")
	if idx < 0 {
		return addr, "", fmt.Errorf("no port in %q", addr)
	}
	return addr[:idx], addr[idx+1:], nil
}

// migrateFromLegacy converts a legacy blocks.db to the new flat-file + LevelDB format.
func migrateFromLegacy(cfg *config.Config, params *fcparams.ChainParams) error {
	// Try both possible legacy locations.
	legacyPath := cfg.LegacyDBPath()
	if _, err := os.Stat(legacyPath); os.IsNotExist(err) {
		legacyPath = cfg.DBPath()
		if _, err := os.Stat(legacyPath); os.IsNotExist(err) {
			return fmt.Errorf("no legacy blocks.db found at %s or %s", cfg.LegacyDBPath(), cfg.DBPath())
		}
	}

	logging.L.Info("migrating from legacy format", "source", legacyPath)

	legacy, err := store.NewBoltStore(legacyPath)
	if err != nil {
		return fmt.Errorf("open legacy store: %w", err)
	}
	defer legacy.Close()

	if !legacy.LegacyHasData() {
		return fmt.Errorf("legacy store has no chain data")
	}

	tipHash, tipHeight, err := legacy.LegacyGetChainTip()
	if err != nil {
		return fmt.Errorf("get legacy chain tip: %w", err)
	}

	logging.L.Info("legacy chain", "tip", tipHash.ReverseString(), "height", tipHeight)

	newStore, err := store.NewFileStore(
		cfg.BlocksDir(),
		cfg.BlockIndexDir(),
		cfg.ChainstateDir(),
		params.NetworkMagic,
	)
	if err != nil {
		return fmt.Errorf("open new store: %w", err)
	}
	defer newStore.Close()

	// Migrate blocks from height 0 to tip.
	// Keep cumulative chainwork exactly like Bitcoin Core's nChainWork:
	// chainWork(block) = chainWork(parent) + work(block).
	cumulativeWork := store.CalcWork(params.GenesisBlock.Header.Bits)
	if tipHeight > 0 {
		// Reset to zero so we accumulate from the first migrated block below.
		cumulativeWork.SetInt64(0)
	}
	for h := uint32(0); h <= tipHeight; h++ {
		hash, err := legacy.LegacyGetBlockByHeight(h)
		if err != nil {
			return fmt.Errorf("get block hash at height %d: %w", h, err)
		}
		block, err := legacy.LegacyGetBlock(hash)
		if err != nil {
			return fmt.Errorf("get block at height %d: %w", h, err)
		}

		fileNum, offset, size, err := newStore.WriteBlock(hash, block)
		if err != nil {
			return fmt.Errorf("write block at height %d: %w", h, err)
		}

		blockWork := store.CalcWork(block.Header.Bits)
		cumulativeWork = new(big.Int).Add(cumulativeWork, blockWork)
		rec := &store.DiskBlockIndex{
			Header:    block.Header,
			Height:    h,
			Status:    store.StatusHaveData | store.StatusValidHeader | store.StatusValidTx,
			TxCount:   uint32(len(block.Transactions)),
			FileNum:   fileNum,
			DataPos:   offset,
			DataSize:  size,
			ChainWork: new(big.Int).Set(cumulativeWork),
		}

		// Migrate undo data if available.
		undoBytes, undoErr := legacy.LegacyGetUndoData(hash)
		if undoErr == nil && len(undoBytes) > 0 {
			undoOffset, undoSize, wErr := newStore.WriteUndo(fileNum, undoBytes)
			if wErr == nil {
				rec.UndoFile = fileNum
				rec.UndoPos = undoOffset
				rec.UndoSize = undoSize
				rec.Status |= store.StatusHaveUndo
			}
		}

		if err := newStore.PutBlockIndex(hash, rec); err != nil {
			return fmt.Errorf("put block index at height %d: %w", h, err)
		}

		if h%1000 == 0 || h == tipHeight {
			logging.L.Info("migration progress", "height", h, "total", tipHeight)
		}
	}

	if err := newStore.PutChainTip(tipHash, tipHeight); err != nil {
		return fmt.Errorf("set chain tip: %w", err)
	}

	logging.L.Info("block migration complete, chain will rebuild UTXO set on next startup")
	return nil
}
