-include .env
export

CHAIN_ID     ?= 46630
RPC_URL      ?= https://rpc.testnet.chain.robinhood.com
VERIFIER_URL ?= https://explorer.testnet.chain.robinhood.com/api/
FORGE_FLAGS  ?= --rpc-url $(RPC_URL) --private-key $(PRIVATE_KEY) --broadcast
VERIFY_FLAGS ?= --verifier blockscout --verifier-url $(VERIFIER_URL) --chain-id $(CHAIN_ID)

.PHONY: help
help:
	@echo ""
	@echo "Weave — available targets"
	@echo ""
	@echo "  Solidity"
	@echo "    make install     install forge deps (OZ v5, chainlink)"
	@echo "    make build       compile all contracts"
	@echo "    make test        run full foundry test suite"
	@echo "    make test-v      run tests with -vvvv verbosity"
	@echo "    make coverage    generate lcov coverage report"
	@echo "    make deploy      run Deploy.s.sol against Robinhood Chain testnet"
	@echo "    make verify ADDR=0x... NAME=src/Contract.sol:Contract"
	@echo "    make fmt         format Solidity via forge fmt"
	@echo "    make snapshot    update .gas-snapshot"
	@echo ""
	@echo "  Go backend"
	@echo "    make go-deps     download Go module dependencies"
	@echo "    make go-build    compile the backend binary"
	@echo "    make go-run      run the backend (requires .env)"
	@echo "    make go-test     run Go unit tests"
	@echo ""
	@echo "  TypeScript agent"
	@echo "    make ts-install  npm install"
	@echo "    make ts-build    tsc compile"
	@echo "    make ts-dev      run agent with ts-node"
	@echo "    make ts-typecheck typecheck without emitting"
	@echo ""
	@echo "  Misc"
	@echo "    make clean       remove build artifacts"
	@echo "    make env         print active env vars"
	@echo ""

.PHONY: install
install:
	forge install OpenZeppelin/openzeppelin-contracts
	forge install smartcontractkit/chainlink

.PHONY: build
build:
	forge build

.PHONY: test
test:
	forge test

.PHONY: test-v
test-v:
	forge test -vvvv

.PHONY: coverage
coverage:
	forge coverage --report lcov

.PHONY: snapshot
snapshot:
	forge snapshot

.PHONY: fmt
fmt:
	forge fmt

.PHONY: deploy
deploy:
	forge script script/Deploy.s.sol:DeployWeave \
		$(FORGE_FLAGS) \
		--slow

.PHONY: verify
verify:
	forge verify-contract $(ADDR) $(NAME) \
		--rpc-url $(RPC_URL) \
		$(VERIFY_FLAGS)

.PHONY: go-deps
go-deps:
	go mod tidy

.PHONY: go-build
go-build:
	go build -o bin/weave-backend ./server/main.go

.PHONY: go-run
go-run:
	go run ./server/main.go

.PHONY: go-test
go-test:
	go test ./server/...

.PHONY: ts-install
ts-install:
	npm install

.PHONY: ts-build
ts-build:
	npm run build

.PHONY: ts-dev
ts-dev:
	npm run dev

.PHONY: ts-typecheck
ts-typecheck:
	npm run typecheck

.PHONY: clean
clean:
	forge clean
	rm -rf dist bin coverage-report lcov.info

.PHONY: env
env:
	@echo "CHAIN_ID  = $(CHAIN_ID)"
	@echo "RPC_URL   = $(RPC_URL)"
	@echo "DEPLOYER  = $(DEPLOYER_ADDRESS)"
	@echo "USDG      = $(USDG_ADDRESS)"