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

	type assetRow struct {
		tokenAddr  string
		oracleAddr string
	}

	rows, err := p.db.Query(
		`SELECT address, oracle_address FROM supported_assets WHERE is_active = 1`,
	)
	if err != nil {
		log.Printf("prices: db query error: %v", err)
		return
	}

	var assets []assetRow
	for rows.Next() {
		var a assetRow
		if err := rows.Scan(&a.tokenAddr, &a.oracleAddr); err != nil {
			continue
		}
		assets = append(assets, a)
	}
	rows.Close()

	now := time.Now().Unix()

	for _, a := range assets {
		price := p.readOraclePrice(ctx, client, common.HexToAddress(a.oracleAddr))
		if price == nil {
			continue
		}

		_, err := p.db.Exec(
			`INSERT INTO price_history (stock_address, price_usdg, timestamp) VALUES (?, ?, ?)`,
			a.tokenAddr,
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