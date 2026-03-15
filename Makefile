.PHONY: all build test clean fairchaind genesis cli adversary chaos

MODULE := github.com/bams-repo/fairchain
BINDIR := bin

all: build

build: fairchaind cli

fairchaind:
	go build -o $(BINDIR)/fairchaind ./cmd/node

genesis:
	go build -o $(BINDIR)/fairchain-genesis ./cmd/genesis

cli:
	go build -o $(BINDIR)/fairchain-cli ./cmd/cli

adversary:
	go build -o $(BINDIR)/fairchain-adversary ./cmd/adversary

chaos: build adversary
	bash scripts/chaos_test.sh

test:
	go test ./... -v -count=1

test-short:
	go test ./... -count=1

bench:
	go test ./... -bench=. -benchmem

clean:
	rm -rf $(BINDIR)
	go clean ./...

lint:
	go vet ./...

fmt:
	gofmt -w .

tidy:
	go mod tidy

# Run a single regtest node with mining enabled.
run-regtest:
	mkdir -p /tmp/fairchain-regtest
	$(BINDIR)/fairchaind \
		-network regtest \
		-datadir /tmp/fairchain-regtest \
		-listen 0.0.0.0:19444 \
		-rpcbind 127.0.0.1 \
		-rpcport 19445 \
		-mine

# Run a second regtest node that connects to the first.
run-regtest2:
	mkdir -p /tmp/fairchain-regtest2
	$(BINDIR)/fairchaind \
		-network regtest \
		-datadir /tmp/fairchain-regtest2 \
		-listen 0.0.0.0:19446 \
		-rpcbind 127.0.0.1 \
		-rpcport 19447 \
		-addnode 127.0.0.1:19444

# --- Testnet targets ---

run-testnet:
	mkdir -p /tmp/fairchain-testnet
	$(BINDIR)/fairchaind \
		-network testnet \
		-datadir /tmp/fairchain-testnet \
		-listen 0.0.0.0:19334 \
		-rpcbind 127.0.0.1 \
		-rpcport 19335 \
		-mine

run-testnet2:
	mkdir -p /tmp/fairchain-testnet2
	$(BINDIR)/fairchaind \
		-network testnet \
		-datadir /tmp/fairchain-testnet2 \
		-listen 0.0.0.0:19336 \
		-rpcbind 127.0.0.1 \
		-rpcport 19337 \
		-addnode 127.0.0.1:19334

testnet-status:
	$(BINDIR)/fairchain-cli -rpcconnect=127.0.0.1 -rpcport=19335 getblockchaininfo

# --- Genesis & status ---

mine-genesis:
	$(BINDIR)/fairchain-genesis --network regtest

mine-genesis-testnet:
	$(BINDIR)/fairchain-genesis --network testnet --timestamp 1773212867 --message "fairchain testnet genesis"

status:
	$(BINDIR)/fairchain-cli -rpcconnect=127.0.0.1 -rpcport=19445 getinfo
