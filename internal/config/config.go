package config

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Config holds all node configuration.
type Config struct {
	// Network selects chain params: "mainnet", "testnet", "regtest".
	Network string `json:"network"`

	// DataDir is the root directory for all node data.
	DataDir string `json:"data_dir"`

	// DataDirName overrides the network subdirectory name (e.g. "testnet2").
	// Set from ChainParams.DataDirName after params are resolved.
	DataDirName string `json:"-"`

	// ListenAddr is the TCP address to listen on for P2P connections.
	ListenAddr string `json:"listen_addr"`

	// RPCAddr is the address for the local RPC/HTTP API.
	RPCAddr string `json:"rpc_addr"`

	// SeedPeers are additional seed peer addresses (IP:port).
	SeedPeers []string `json:"seed_peers"`

	// MaxInbound is the maximum number of inbound peer connections.
	MaxInbound int `json:"max_inbound"`

	// MaxOutbound is the maximum number of outbound peer connections.
	MaxOutbound int `json:"max_outbound"`

	// MiningEnabled controls whether the built-in miner runs.
	MiningEnabled bool `json:"mining_enabled"`

	// MiningAddr is the reward address/script placeholder for mined blocks.
	MiningAddr string `json:"mining_addr"`

	// LogLevel controls logging verbosity: "debug", "info", "warn", "error".
	LogLevel string `json:"log_level"`

	// RPCUser is the username for HTTP Basic Auth on the RPC server.
	// If both RPCUser and RPCPassword are empty, a random cookie file is generated.
	RPCUser string `json:"rpc_user,omitempty"`

	// RPCPassword is the password for HTTP Basic Auth on the RPC server.
	RPCPassword string `json:"rpc_password,omitempty"`
}

// DefaultConfig returns a config with sensible defaults for regtest.
func DefaultConfig() *Config {
	return &Config{
		Network:       "regtest",
		DataDir:       defaultDataDir(),
		ListenAddr:    "0.0.0.0:19444",
		RPCAddr:       "127.0.0.1:19445",
		SeedPeers:     []string{},
		MaxInbound:    25,
		MaxOutbound:   8,
		MiningEnabled: false,
		MiningAddr:    "",
		LogLevel:      "info",
	}
}

// LoadConfig reads a config from a JSON file, falling back to defaults for missing fields.
func LoadConfig(path string) (*Config, error) {
	cfg := DefaultConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, fmt.Errorf("read config: %w", err)
	}

	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	return cfg, nil
}

// SaveConfig writes the config to a JSON file.
func SaveConfig(path string, cfg *Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// LoadConf reads a fairchain.conf INI-style config file.
// Supports network sections: [main], [test], [regtest].
// Options use the same names as CLI flags (without --).
// Priority: CLI > conf > defaults.
func LoadConf(path string, network string) (*Config, error) {
	cfg := DefaultConfig()

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, fmt.Errorf("open conf: %w", err)
	}
	defer f.Close()

	sectionForNetwork := confSectionName(network)
	currentSection := ""
	scanner := bufio.NewScanner(f)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			currentSection = strings.ToLower(strings.Trim(line, "[]"))
			continue
		}

		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])

		// Apply if global (no section) or matching the target network section.
		if currentSection != "" && currentSection != sectionForNetwork {
			continue
		}

		applyConfOption(cfg, key, val)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read conf: %w", err)
	}

	return cfg, nil
}

func confSectionName(network string) string {
	switch network {
	case "mainnet":
		return "main"
	case "testnet":
		return "test"
	case "regtest":
		return "regtest"
	default:
		return network
	}
}

func applyConfOption(cfg *Config, key, val string) {
	switch key {
	case "network":
		cfg.Network = val
	case "datadir":
		cfg.DataDir = val
	case "listen":
		cfg.ListenAddr = val
	case "rpc":
		cfg.RPCAddr = val
	case "mine":
		cfg.MiningEnabled = val == "1" || val == "true"
	case "miningaddr":
		cfg.MiningAddr = val
	case "loglevel":
		cfg.LogLevel = val
	case "maxinbound":
		fmt.Sscanf(val, "%d", &cfg.MaxInbound)
	case "maxoutbound":
		fmt.Sscanf(val, "%d", &cfg.MaxOutbound)
	case "seedpeers":
		cfg.SeedPeers = strings.Split(val, ",")
	case "rpcuser":
		cfg.RPCUser = val
	case "rpcpassword":
		cfg.RPCPassword = val
	}
}

// NetworkDataDir returns the network-specific data directory.
// Bitcoin Core convention: mainnet uses the root, others get a subdirectory
// whose name is controlled by ChainParams.DataDirName (e.g. "testnet2").
func (c *Config) NetworkDataDir() string {
	if c.DataDirName == "" {
		return c.DataDir
	}
	return filepath.Join(c.DataDir, c.DataDirName)
}

// BlocksDir returns the path to the blocks/ directory (blk*.dat, rev*.dat).
func (c *Config) BlocksDir() string {
	return filepath.Join(c.NetworkDataDir(), "blocks")
}

// BlockIndexDir returns the path to the blocks/index/ LevelDB directory.
func (c *Config) BlockIndexDir() string {
	return filepath.Join(c.BlocksDir(), "index")
}

// ChainstateDir returns the path to the chainstate/ LevelDB directory.
func (c *Config) ChainstateDir() string {
	return filepath.Join(c.NetworkDataDir(), "chainstate")
}

// PeerDBPath returns the path to the peer database (bbolt).
func (c *Config) PeerDBPath() string {
	return filepath.Join(c.NetworkDataDir(), "peers.db")
}

// PeersDatPath returns the path to the peers.dat flat-file dump.
func (c *Config) PeersDatPath() string {
	return filepath.Join(c.NetworkDataDir(), "peers.dat")
}

// MempoolPath returns the path to the mempool.dat persistence file.
func (c *Config) MempoolPath() string {
	return filepath.Join(c.NetworkDataDir(), "mempool.dat")
}

// LockFilePath returns the path to the .lock file.
func (c *Config) LockFilePath() string {
	return filepath.Join(c.NetworkDataDir(), ".lock")
}

// RPCCookiePath returns the path to the .cookie file for RPC auth.
func (c *Config) RPCCookiePath() string {
	return filepath.Join(c.NetworkDataDir(), ".cookie")
}

// ConfFilePath returns the path to fairchain.conf in the data directory root.
func (c *Config) ConfFilePath() string {
	return filepath.Join(c.DataDir, "fairchain.conf")
}

// DBPath returns the legacy block database path (for migration detection).
func (c *Config) DBPath() string {
	return filepath.Join(c.DataDir, "blocks.db")
}

// LegacyDBPath returns the legacy block database path in the network-specific dir.
func (c *Config) LegacyDBPath() string {
	return filepath.Join(c.NetworkDataDir(), "blocks.db")
}

// WalletDir returns the path to the wallet directory for key storage.
func (c *Config) WalletDir() string {
	return filepath.Join(c.NetworkDataDir(), "wallet")
}

// EnsureDataDir creates the full data directory tree for the current network.
func (c *Config) EnsureDataDir() error {
	dirs := []string{
		c.NetworkDataDir(),
		c.BlocksDir(),
		c.BlockIndexDir(),
		c.ChainstateDir(),
		c.WalletDir(),
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return fmt.Errorf("create directory %s: %w", dir, err)
		}
	}
	return nil
}

func defaultDataDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".fairchain"
	}
	return filepath.Join(home, ".fairchain")
}
