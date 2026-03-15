package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/bams-repo/fairchain/internal/version"
)

func main() {
	rpcConnect := flag.String("rpcconnect", "127.0.0.1", "RPC server host")
	rpcPort := flag.String("rpcport", "19445", "RPC server port")
	printVer := flag.Bool("version", false, "Print version and exit")
	flag.Parse()

	if *printVer {
		fmt.Printf("Fairchain CLI version v%s\n", version.String())
		os.Exit(0)
	}

	args := flag.Args()
	if len(args) == 0 {
		printUsage()
		os.Exit(1)
	}

	baseURL := fmt.Sprintf("http://%s:%s", *rpcConnect, *rpcPort)
	command := strings.ToLower(args[0])
	params := args[1:]

	endpoint, err := resolveEndpoint(command, params)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	resp, err := http.Get(baseURL + endpoint)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: Could not connect to the server %s\n", baseURL)
		fmt.Fprintf(os.Stderr, "       Is fairchaind running?\n")
		os.Exit(1)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if resp.StatusCode != http.StatusOK {
		var errResp map[string]string
		if json.Unmarshal(body, &errResp) == nil {
			if msg, ok := errResp["error"]; ok {
				fmt.Fprintf(os.Stderr, "error: %s\n", msg)
				os.Exit(1)
			}
		}
		fmt.Fprintf(os.Stderr, "error code: %d\n%s\n", resp.StatusCode, string(body))
		os.Exit(1)
	}

	formatOutput(body)
}

func resolveEndpoint(command string, params []string) (string, error) {
	switch command {

	// --- Blockchain ---
	case "getblockchaininfo":
		return "/getblockchaininfo", nil
	case "getblockcount":
		return "/getblockcount", nil
	case "getbestblockhash":
		return "/getbestblockhash", nil
	case "getblockhash":
		if len(params) < 1 {
			return "", fmt.Errorf("getblockhash requires <height>")
		}
		return "/getblockhash?height=" + url.QueryEscape(params[0]), nil
	case "getblock":
		if len(params) < 1 {
			return "", fmt.Errorf("getblock requires <hash>")
		}
		return "/getblock?hash=" + url.QueryEscape(params[0]), nil
	case "getblockbyheight":
		if len(params) < 1 {
			return "", fmt.Errorf("getblockbyheight requires <height>")
		}
		return "/getblockbyheight?height=" + url.QueryEscape(params[0]), nil
	case "getdifficulty":
		return "/getdifficulty", nil

	// --- Network ---
	case "getnetworkinfo":
		return "/getnetworkinfo", nil
	case "getpeerinfo":
		return "/getpeerinfo", nil
	case "getconnectioncount":
		return "/getconnectioncount", nil
	case "addnode":
		if len(params) < 1 {
			return "", fmt.Errorf("addnode requires <ip:port>")
		}
		return "/addnode?node=" + url.QueryEscape(params[0]), nil
	case "disconnectnode":
		if len(params) < 1 {
			return "", fmt.Errorf("disconnectnode requires <address>")
		}
		return "/disconnectnode?address=" + url.QueryEscape(params[0]), nil

	// --- Mempool ---
	case "getmempoolinfo":
		return "/getmempoolinfo", nil
	case "getrawmempool":
		verbose := ""
		if len(params) > 0 && params[0] == "true" {
			verbose = "?verbose=true"
		}
		return "/getrawmempool" + verbose, nil
	case "getmempoolentry":
		if len(params) < 1 {
			return "", fmt.Errorf("getmempoolentry requires <txid>")
		}
		return "/getmempoolentry?txid=" + url.QueryEscape(params[0]), nil

	// --- UTXO ---
	case "gettxout":
		if len(params) < 2 {
			return "", fmt.Errorf("gettxout requires <txid> <n>")
		}
		return "/gettxout?txid=" + url.QueryEscape(params[0]) + "&n=" + url.QueryEscape(params[1]), nil
	case "gettxoutsetinfo":
		return "/gettxoutsetinfo", nil

	// --- Mining ---
	case "submitblock":
		return "", fmt.Errorf("submitblock requires POST — use curl or the RPC directly")

	// --- Control ---
	case "getinfo":
		return "/getinfo", nil
	case "stop":
		return "/stop", nil
	case "help":
		printUsage()
		os.Exit(0)
		return "", nil

	// --- Wallet ---
	case "getnewaddress":
		return "/getnewaddress", nil
	case "getbalance":
		minconf := "1"
		if len(params) > 0 {
			minconf = params[0]
		}
		return "/getbalance?minconf=" + url.QueryEscape(minconf), nil
	case "listunspent":
		q := "/listunspent"
		if len(params) >= 1 {
			q += "?minconf=" + url.QueryEscape(params[0])
		}
		if len(params) >= 2 {
			if strings.Contains(q, "?") {
				q += "&"
			} else {
				q += "?"
			}
			q += "maxconf=" + url.QueryEscape(params[1])
		}
		return q, nil
	case "sendtoaddress":
		if len(params) < 2 {
			return "", fmt.Errorf("sendtoaddress requires <address> <amount>")
		}
		return "/sendtoaddress?address=" + url.QueryEscape(params[0]) + "&amount=" + url.QueryEscape(params[1]), nil
	case "getwalletinfo":
		return "/getwalletinfo", nil
	case "dumpprivkey":
		if len(params) < 1 {
			return "", fmt.Errorf("dumpprivkey requires <address>")
		}
		return "/dumpprivkey?address=" + url.QueryEscape(params[0]), nil
	case "importprivkey":
		if len(params) < 1 {
			return "", fmt.Errorf("importprivkey requires <privkey>")
		}
		return "/importprivkey?privkey=" + url.QueryEscape(params[0]), nil
	case "validateaddress":
		if len(params) < 1 {
			return "", fmt.Errorf("validateaddress requires <address>")
		}
		return "/validateaddress?address=" + url.QueryEscape(params[0]), nil
	case "getrawchangeaddress":
		return "/getrawchangeaddress", nil
	case "settxfee":
		if len(params) < 1 {
			return "", fmt.Errorf("settxfee requires <amount>")
		}
		return "/settxfee?amount=" + url.QueryEscape(params[0]), nil
	case "sendrawtransaction":
		if len(params) < 1 {
			return "", fmt.Errorf("sendrawtransaction requires <hexstring>")
		}
		return "/sendrawtransaction?hexstring=" + url.QueryEscape(params[0]), nil
	case "dumpwallet":
		return "/dumpwallet", nil
	case "signrawtransactionwithwallet":
		if len(params) < 1 {
			return "", fmt.Errorf("signrawtransactionwithwallet requires <hexstring>")
		}
		return "/signrawtransactionwithwallet?hexstring=" + url.QueryEscape(params[0]), nil
	case "getreceivedbyaddress":
		if len(params) < 1 {
			return "", fmt.Errorf("getreceivedbyaddress requires <address> [minconf]")
		}
		q := "/getreceivedbyaddress?address=" + url.QueryEscape(params[0])
		if len(params) >= 2 {
			q += "&minconf=" + url.QueryEscape(params[1])
		}
		return q, nil
	case "listaddressgroupings":
		return "/listaddressgroupings", nil
	case "backupwallet":
		if len(params) < 1 {
			return "", fmt.Errorf("backupwallet requires <destination>")
		}
		return "/backupwallet?destination=" + url.QueryEscape(params[0]), nil
	case "getaddressesbylabel":
		return "/getaddressesbylabel", nil
	case "listtransactions":
		q := "/listtransactions"
		if len(params) > 0 {
			q += "?count=" + url.QueryEscape(params[0])
		}
		return q, nil
	case "gettransaction":
		if len(params) < 1 {
			return "", fmt.Errorf("gettransaction requires <txid>")
		}
		return "/gettransaction?txid=" + url.QueryEscape(params[0]), nil
	case "encryptwallet":
		if len(params) < 1 {
			return "", fmt.Errorf("encryptwallet requires <passphrase>")
		}
		return "/encryptwallet?passphrase=" + url.QueryEscape(params[0]), nil
	case "walletpassphrase":
		if len(params) < 1 {
			return "", fmt.Errorf("walletpassphrase requires <passphrase> [timeout]")
		}
		q := "/walletpassphrase?passphrase=" + url.QueryEscape(params[0])
		if len(params) >= 2 {
			q += "&timeout=" + url.QueryEscape(params[1])
		}
		return q, nil
	case "walletlock":
		return "/walletlock", nil

	// --- Fairchain-specific ---
	case "getchainstatus":
		return "/getchainstatus", nil
	case "metrics":
		return "/metrics", nil

	default:
		return "", fmt.Errorf("unknown command: %s\nRun 'fairchain-cli help' for usage", command)
	}
}

func formatOutput(body []byte) {
	var data interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		fmt.Println(string(body))
		return
	}

	switch v := data.(type) {
	case string:
		fmt.Println(v)
	case float64:
		if v == float64(int64(v)) {
			fmt.Printf("%d\n", int64(v))
		} else {
			fmt.Printf("%v\n", v)
		}
	default:
		pretty, _ := json.MarshalIndent(data, "", "  ")
		fmt.Println(string(pretty))
	}
}

func printUsage() {
	fmt.Println("Fairchain CLI v" + version.String())
	fmt.Println()
	fmt.Println("Usage: fairchain-cli [options] <command> [params]")
	fmt.Println()
	fmt.Println("Options:")
	fmt.Println("  -rpcconnect=<ip>    Connect to RPC at <ip> (default: 127.0.0.1)")
	fmt.Println("  -rpcport=<port>     Connect to RPC on <port> (default: 19445)")
	fmt.Println("  -version            Print version and exit")
	fmt.Println()
	fmt.Println("Blockchain commands:")
	fmt.Println("  getblockchaininfo              Get blockchain state")
	fmt.Println("  getblockcount                  Get current block height")
	fmt.Println("  getbestblockhash               Get hash of best block")
	fmt.Println("  getblockhash <height>          Get block hash at height")
	fmt.Println("  getblock <hash>                Get block data by hash")
	fmt.Println("  getblockbyheight <height>      Get block data by height")
	fmt.Println("  getdifficulty                  Get current difficulty")
	fmt.Println()
	fmt.Println("Network commands:")
	fmt.Println("  getnetworkinfo                 Get network state")
	fmt.Println("  getpeerinfo                    Get connected peer details")
	fmt.Println("  getconnectioncount             Get number of connections")
	fmt.Println("  addnode <ip:port>              Connect to a node")
	fmt.Println("  disconnectnode <addr>          Disconnect a peer")
	fmt.Println()
	fmt.Println("Mempool commands:")
	fmt.Println("  getmempoolinfo                 Get mempool state")
	fmt.Println("  getrawmempool [true]           List mempool txids (verbose=true for details)")
	fmt.Println("  getmempoolentry <txid>         Get mempool entry for a transaction")
	fmt.Println()
	fmt.Println("UTXO commands:")
	fmt.Println("  gettxout <txid> <n>            Get unspent output")
	fmt.Println("  gettxoutsetinfo                Get UTXO set statistics")
	fmt.Println()
	fmt.Println("Wallet commands:")
	fmt.Println("  getnewaddress                  Generate a new receiving address")
	fmt.Println("  getbalance [minconf]           Get wallet balance (default minconf=1)")
	fmt.Println("  listunspent [minconf] [maxconf]  List unspent outputs")
	fmt.Println("  sendtoaddress <addr> <amount>  Send coins to an address")
	fmt.Println("  getwalletinfo                  Get wallet information")
	fmt.Println("  dumpprivkey <address>          Dump private key (WIF format)")
	fmt.Println("  importprivkey <key>            Import a private key (WIF or hex)")
	fmt.Println("  validateaddress <address>      Validate an address")
	fmt.Println("  getrawchangeaddress            Get a new change address")
	fmt.Println("  settxfee <amount>              Set transaction fee per byte")
	fmt.Println("  sendrawtransaction <hex>       Submit a raw transaction")
	fmt.Println("  signrawtransactionwithwallet <hex>  Sign a raw transaction with wallet keys")
	fmt.Println("  getreceivedbyaddress <addr> [minconf]  Total received by address")
	fmt.Println("  listaddressgroupings           List address groupings with balances")
	fmt.Println("  backupwallet <destination>     Backup wallet to file")
	fmt.Println("  getaddressesbylabel            List addresses by label")
	fmt.Println("  listtransactions [count]       List recent wallet transactions")
	fmt.Println("  gettransaction <txid>          Get transaction details")
	fmt.Println("  dumpwallet                     Dump wallet info (mnemonic, addresses)")
	fmt.Println("  encryptwallet <passphrase>     Encrypt the wallet with a passphrase")
	fmt.Println("  walletpassphrase <pass> [secs] Unlock wallet for <secs> seconds")
	fmt.Println("  walletlock                     Lock the wallet")
	fmt.Println()
	fmt.Println("Control commands:")
	fmt.Println("  getinfo                        Get node overview")
	fmt.Println("  stop                           Stop the daemon")
	fmt.Println("  help                           Show this help")
	fmt.Println()
	fmt.Println("Fairchain-specific commands:")
	fmt.Println("  getchainstatus                 Get chain status (bits, retarget, peers)")
	fmt.Println("  metrics                        Get internal metrics")
}
