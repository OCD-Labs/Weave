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

.PHONY: build-contracts
build-contracts:
	FOUNDRY_PROFILE=contracts forge build

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
		--gas-estimate-multiplier 500 \
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
	cd server && go test ./... -v -count=1

.PHONY: go-fuzz
go-fuzz:
	cd server && go test ./indexer -fuzz=FuzzDecodeAssetAddedData -fuzztime=30s
	cd server && go test ./indexer -fuzz=FuzzDecodeBigIntFromLogData -fuzztime=30s

.PHONY: clean
clean:
	forge clean
	rm -rf dist bin coverage-report lcov.info

# Emergency: pull all funded tokens back from the SwapRouter to your wallet.
# Usage: make router-withdraw-all
.PHONY: router-withdraw-all
router-withdraw-all:
	cast send $(SWAP_ROUTER_ADDRESS) \
		"withdrawAll(address[])" \
		"[$(USDG_ADDRESS),$(TSLA_ADDRESS),$(AMZN_ADDRESS),$(PLTR_ADDRESS),$(NFLX_ADDRESS),$(AMD_ADDRESS)]" \
		--rpc-url $(RPC_URL) \
		--private-key $(PRIVATE_KEY)

# Withdraw a single token: make router-withdraw TOKEN=0x... AMOUNT=1000000000000000000
.PHONY: router-withdraw
router-withdraw:
	cast send $(SWAP_ROUTER_ADDRESS) \
		"withdraw(address,uint256)" \
		$(TOKEN) $(AMOUNT) \
		--rpc-url $(RPC_URL) \
		--private-key $(PRIVATE_KEY)

# Redeem all your basket tokens from a deployed basket back to USDG.
# Usage: make basket-redeem BASKET_ADDRESS=0x... BASKET_TOKEN_AMOUNT=<18dec amount>
.PHONY: basket-redeem
basket-redeem:
	cast send $(BASKET_ADDRESS) \
		"redeem(uint256,uint256,address)" \
		$(BASKET_TOKEN_AMOUNT) 0 $(DEPLOYER_ADDRESS) \
		--rpc-url $(RPC_URL) \
		--private-key $(PRIVATE_KEY)

# Claim all accumulated creator revenue from a creator token contract.
# Usage: make creator-claim CREATOR_TOKEN_ADDRESS=0x...
.PHONY: creator-claim
creator-claim:
	cast send $(CREATOR_TOKEN_ADDRESS) \
		"claimAll()" \
		--rpc-url $(RPC_URL) \
		--private-key $(PRIVATE_KEY)

.PHONY: update-prices
update-prices:
	cast send $(ORACLE_TSLA) "setPrice(int256)" $(TSLA_PRICE_8DEC) --rpc-url $(RPC_URL) --private-key $(PRIVATE_KEY)
	cast send $(ORACLE_AMZN) "setPrice(int256)" $(AMZN_PRICE_8DEC) --rpc-url $(RPC_URL) --private-key $(PRIVATE_KEY)
	cast send $(ORACLE_PLTR) "setPrice(int256)" $(PLTR_PRICE_8DEC) --rpc-url $(RPC_URL) --private-key $(PRIVATE_KEY)
	cast send $(ORACLE_NFLX) "setPrice(int256)" $(NFLX_PRICE_8DEC) --rpc-url $(RPC_URL) --private-key $(PRIVATE_KEY)
	cast send $(ORACLE_AMD)  "setPrice(int256)" $(AMD_PRICE_8DEC)  --rpc-url $(RPC_URL) --private-key $(PRIVATE_KEY)

.PHONY: fund-router
fund-router:
	cast send $(TSLA_ADDRESS) "approve(address,uint256)" $(SWAP_ROUTER_ADDRESS) 5000000000000000000 --rpc-url $(RPC_URL) --private-key $(PRIVATE_KEY)
	cast send $(AMZN_ADDRESS) "approve(address,uint256)" $(SWAP_ROUTER_ADDRESS) 5000000000000000000 --rpc-url $(RPC_URL) --private-key $(PRIVATE_KEY)
	cast send $(PLTR_ADDRESS) "approve(address,uint256)" $(SWAP_ROUTER_ADDRESS) 5000000000000000000 --rpc-url $(RPC_URL) --private-key $(PRIVATE_KEY)
	cast send $(NFLX_ADDRESS) "approve(address,uint256)" $(SWAP_ROUTER_ADDRESS) 5000000000000000000 --rpc-url $(RPC_URL) --private-key $(PRIVATE_KEY)
	cast send $(AMD_ADDRESS)  "approve(address,uint256)" $(SWAP_ROUTER_ADDRESS) 5000000000000000000 --rpc-url $(RPC_URL) --private-key $(PRIVATE_KEY)
	cast send $(USDG_ADDRESS) "approve(address,uint256)" $(SWAP_ROUTER_ADDRESS) 50000000 --rpc-url $(RPC_URL) --private-key $(PRIVATE_KEY)
	cast send $(SWAP_ROUTER_ADDRESS) "fund(address,uint256)" $(TSLA_ADDRESS) 5000000000000000000 --rpc-url $(RPC_URL) --private-key $(PRIVATE_KEY)
	cast send $(SWAP_ROUTER_ADDRESS) "fund(address,uint256)" $(AMZN_ADDRESS) 5000000000000000000 --rpc-url $(RPC_URL) --private-key $(PRIVATE_KEY)
	cast send $(SWAP_ROUTER_ADDRESS) "fund(address,uint256)" $(PLTR_ADDRESS) 5000000000000000000 --rpc-url $(RPC_URL) --private-key $(PRIVATE_KEY)
	cast send $(SWAP_ROUTER_ADDRESS) "fund(address,uint256)" $(NFLX_ADDRESS) 5000000000000000000 --rpc-url $(RPC_URL) --private-key $(PRIVATE_KEY)
	cast send $(SWAP_ROUTER_ADDRESS) "fund(address,uint256)" $(AMD_ADDRESS)  5000000000000000000 --rpc-url $(RPC_URL) --private-key $(PRIVATE_KEY)
	cast send $(SWAP_ROUTER_ADDRESS) "fund(address,uint256)" $(USDG_ADDRESS) 50000000 --rpc-url $(RPC_URL) --private-key $(PRIVATE_KEY)

.PHONY: env
env:
	@echo "CHAIN_ID  = $(CHAIN_ID)"
	@echo "RPC_URL   = $(RPC_URL)"
	@echo "DEPLOYER  = $(DEPLOYER_ADDRESS)"
	@echo "USDG      = $(USDG_ADDRESS)"