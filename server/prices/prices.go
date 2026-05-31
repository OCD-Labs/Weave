// backend/prices/prices.go

package prices

import (
	"context"
	"log"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/OCD-Labs/Weave/server/db"
)

// latestPriceABI is the ABI fragment for IWeaveOracle.latestPrice().
var latestPriceABI, _ = abi.JSON(strings.NewReader(`[{
	"inputs": [],
	"name": "latestPrice",
	"outputs": [
		{"internalType": "int256",  "name": "price",     "type": "int256"},
		{"internalType": "uint256", "name": "updatedAt", "type": "uint256"}
	],
	"stateMutability": "view",
	"type": "function"
}]`))

// getSupportedAssetsABI is the ABI fragment for IWeaveRegistry.getSupportedAssets().
var getSupportedAssetsABI, _ = abi.JSON(strings.NewReader(`[{
	"inputs": [],
	"name": "getSupportedAssets",
	"outputs": [{
		"components": [
			{"internalType": "address", "name": "tokenAddress", "type": "address"},
			{"internalType": "address", "name": "oracle",       "type": "address"},
			{"internalType": "string",  "name": "symbol",       "type": "string"},
			{"internalType": "string",  "name": "name",         "type": "string"},
			{"internalType": "string",  "name": "sector",       "type": "string"},
			{"internalType": "bool",    "name": "active",       "type": "bool"}
		],
		"internalType": "struct IWeaveRegistry.AssetConfig[]",
		"name": "",
		"type": "tuple[]"
	}],
	"stateMutability": "view",
	"type": "function"
}]`))

// Poller reads oracle prices at a fixed interval and writes to price_history.
type Poller struct {
	rpcURL       string
	registryAddr common.Address
	db           *db.DB
	interval     time.Duration
}

func NewPoller(rpcURL, registryAddr string, database *db.DB, interval time.Duration) *Poller {
	return &Poller{
		rpcURL:       rpcURL,
		registryAddr: common.HexToAddress(registryAddr),
		db:           database,
		interval:     interval,
	}
}

// Run polls prices on the configured interval until ctx is cancelled.
func (p *Poller) Run(ctx context.Context) {
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	// Poll once immediately on startup.
	p.poll(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.poll(ctx)
		}
	}
}

func (p *Poller) poll(ctx context.Context) {
	client, err := ethclient.DialContext(ctx, p.rpcURL)
	if err != nil {
		log.Printf("prices: dial error: %v", err)
		return
	}
	defer client.Close()

	caller := bind.NewBoundContract(p.registryAddr, getSupportedAssetsABI, client, nil, nil)

	var results []interface{}
	err = caller.Call(&bind.CallOpts{Context: ctx}, &results, "getSupportedAssets")
	if err != nil {
		log.Printf("prices: getSupportedAssets error: %v", err)
		return
	}

	if len(results) == 0 {
		return
	}

	// The result is a slice of structs. Each struct has fields matching AssetConfig.
	type assetConfig struct {
		TokenAddress common.Address
		Oracle       common.Address
		Symbol       string
		Name         string
		Sector       string
		Active       bool
	}

	assets, ok := results[0].([]struct {
		TokenAddress common.Address `abi:"tokenAddress"`
		Oracle       common.Address `abi:"oracle"`
		Symbol       string         `abi:"symbol"`
		Name         string         `abi:"name"`
		Sector       string         `abi:"sector"`
		Active       bool           `abi:"active"`
	})
	if !ok {
		log.Printf("prices: unexpected type from getSupportedAssets")
		return
	}

	now := time.Now().Unix()

	for _, asset := range assets {
		if !asset.Active {
			continue
		}

		price := p.readOraclePrice(ctx, client, asset.Oracle)
		if price == nil {
			continue
		}

		_, err := p.db.Exec(
			`INSERT INTO price_history (stock_address, price_usdg, timestamp) VALUES (?, ?, ?)`,
			strings.ToLower(asset.TokenAddress.Hex()),
			price.String(),
			now,
		)
		if err != nil {
			log.Printf("prices: insert price_history: %v", err)
		}
	}
}

func (p *Poller) readOraclePrice(ctx context.Context, client *ethclient.Client, oracle common.Address) *big.Int {
	caller := bind.NewBoundContract(oracle, latestPriceABI, client, nil, nil)

	var results []interface{}
	err := caller.Call(&bind.CallOpts{Context: ctx}, &results, "latestPrice")
	if err != nil {
		log.Printf("prices: latestPrice(%s): %v", oracle.Hex(), err)
		return nil
	}

	if len(results) < 1 {
		return nil
	}

	price, ok := results[0].(*big.Int)
	if !ok || price == nil || price.Sign() <= 0 {
		return nil
	}

	return price
}