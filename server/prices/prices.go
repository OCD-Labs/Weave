package prices

import (
	"context"
	"log"
	"math/big"
	"strings"
	"time"

	"github.com/OCD-Labs/Weave/server/db"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
)

// latestRoundDataABI is the ABI fragment for IWeaveOracle.latestRoundData(),
// matching Chainlink AggregatorV3Interface exactly. On mainnet, any Chainlink
// aggregator address can be used directly without an adapter contract.
var latestRoundDataABI, _ = abi.JSON(strings.NewReader(`[{
	"inputs": [],
	"name": "latestRoundData",
	"outputs": [
		{"internalType": "uint80",  "name": "roundId",         "type": "uint80"},
		{"internalType": "int256",  "name": "answer",          "type": "int256"},
		{"internalType": "uint256", "name": "startedAt",       "type": "uint256"},
		{"internalType": "uint256", "name": "updatedAt",       "type": "uint256"},
		{"internalType": "uint80",  "name": "answeredInRound", "type": "uint80"}
	],
	"stateMutability": "view",
	"type": "function"
}]`))

// Poller reads oracle prices at a fixed interval and writes to price_history.
// It only polls assets that are constituents of at least one active (non-suspended)
// basket — idle catalogue assets generate no polling traffic.
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

	// Poll once immediately on startup so charts have data before the first tick.
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

	// Only poll assets that are constituents of at least one active basket.
	// This eliminates polling overhead for catalogue assets that no basket holds,
	// and keeps price_history focused on data the frontend actually queries.
	rows, err := p.db.Query(`
		SELECT DISTINCT sa.address, sa.oracle_address
		FROM supported_assets sa
		INNER JOIN basket_constituents bc ON bc.stock_address = sa.address
		INNER JOIN baskets b ON b.address = bc.basket_address AND b.suspended = 0
		WHERE sa.is_active = 1`,
	)
	if err != nil {
		log.Printf("prices: db query error: %v", err)
		return
	}

	type assetRow struct {
		tokenAddr  string
		oracleAddr string
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

	log.Printf("prices: polled %d active constituent assets", len(assets))
}

// readOraclePrice calls latestRoundData() on the oracle contract and returns
// the answer field (index 1 in the 5-tuple return). 
func (p *Poller) readOraclePrice(ctx context.Context, client *ethclient.Client, oracle common.Address) *big.Int {
	caller := bind.NewBoundContract(oracle, latestRoundDataABI, client, nil, nil)

	var results []interface{}
	err := caller.Call(&bind.CallOpts{Context: ctx}, &results, "latestRoundData")
	if err != nil {
		log.Printf("prices: latestRoundData(%s): %v", oracle.Hex(), err)
		return nil
	}

	// Return tuple: (roundId, answer, startedAt, updatedAt, answeredInRound)
	if len(results) < 2 {
		log.Printf("prices: latestRoundData(%s): unexpected result count %d", oracle.Hex(), len(results))
		return nil
	}

	price, ok := results[1].(*big.Int)
	if !ok || price == nil || price.Sign() <= 0 {
		log.Printf("prices: latestRoundData(%s): invalid answer value", oracle.Hex())
		return nil
	}

	return price
}