package nav

import (
	"context"
	"log"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/OCD-Labs/Weave/server/db"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
)

var basketViewABI, _ = abi.JSON(strings.NewReader(`[
	{
		"inputs": [],
		"name": "navPerToken",
		"outputs": [{"internalType":"uint256","name":"","type":"uint256"}],
		"stateMutability": "view",
		"type": "function"
	},
	{
		"inputs": [],
		"name": "totalValueUsdg",
		"outputs": [{"internalType":"uint256","name":"","type":"uint256"}],
		"stateMutability": "view",
		"type": "function"
	}
]`))

// Poller reads navPerToken and totalValueUsdg from every active basket on
// each interval tick and writes the result to nav_history.
type Poller struct {
	rpcURL   string
	db       *db.DB
	interval time.Duration
}

func NewPoller(rpcURL string, database *db.DB, interval time.Duration) *Poller {
	return &Poller{
		rpcURL:   rpcURL,
		db:       database,
		interval: interval,
	}
}

func (p *Poller) Run(ctx context.Context) {
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	// Poll immediately on startup so charts have data before the first tick.
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
	// Nav polling must cover every basket on every cycle.
	rows, err := p.db.Query(`SELECT address FROM baskets WHERE suspended = 0`)
	if err != nil {
		log.Printf("nav: db query error: %v", err)
		return
	}

	var addrs []string
	for rows.Next() {
		var addr string
		if rows.Scan(&addr) == nil {
			addrs = append(addrs, addr)
		}
	}
	rows.Close()

	if len(addrs) == 0 {
		log.Printf("nav: no active baskets to poll")
		return
	}

	work := make(chan string, len(addrs))
	for _, addr := range addrs {
		work <- addr
	}
	close(work)

	const workers = 5

	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Each worker gets its own RPC connection to avoid contention.
			client, err := ethclient.DialContext(ctx, p.rpcURL)
			if err != nil {
				log.Printf("nav: worker dial error: %v", err)
				return
			}
			defer client.Close()

			for basketAddr := range work {
				p.pollOne(ctx, client, basketAddr)
			}
		}()
	}
	wg.Wait()

	log.Printf("nav: poll cycle complete — %d baskets", len(addrs))
}

func (p *Poller) pollOne(ctx context.Context, client *ethclient.Client, basketAddr string) {
	addr := common.HexToAddress(basketAddr)

	nav := p.callUint256(ctx, client, addr, "navPerToken")
	if nav == nil {
		return
	}

	totalValue := p.callUint256(ctx, client, addr, "totalValueUsdg")
	if totalValue == nil {
		return
	}

	_, err := p.db.Exec(`
		INSERT INTO nav_history (basket_address, nav_per_token, total_value_usdg, timestamp)
		VALUES (?, ?, ?, ?)`,
		basketAddr,
		nav.String(),
		totalValue.String(),
		time.Now().Unix(),
	)
	if err != nil {
		log.Printf("nav: insert nav_history(%s): %v", basketAddr, err)
	}
}

func (p *Poller) callUint256(ctx context.Context, client *ethclient.Client, addr common.Address, method string) *big.Int {
	data, err := client.CallContract(ctx, ethereum.CallMsg{
		To:   &addr,
		Data: basketViewABI.Methods[method].ID,
	}, nil)
	if err != nil {
		log.Printf("nav: %s(%s): %v", method, addr.Hex(), err)
		return nil
	}

	unpacked, err := basketViewABI.Methods[method].Outputs.Unpack(data)
	if err != nil || len(unpacked) == 0 {
		log.Printf("nav: %s(%s) unpack: %v", method, addr.Hex(), err)
		return nil
	}

	result, ok := unpacked[0].(*big.Int)
	if !ok || result == nil {
		return nil
	}

	return result
}