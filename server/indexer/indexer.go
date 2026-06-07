package indexer

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"math/big"
	"os"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/OCD-Labs/Weave/server/db"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
)

// rpcTimeout is the per-call deadline applied to every RPC request.
// Prevents any single slow or stalled node from blocking the indexer indefinitely.
const rpcTimeout = 15 * time.Second

// chunkSize is the number of blocks fetched per FilterLogs call.
const chunkSize = int64(2000)

// seedMaxAttempts is the maximum number of times seedOneBasket will be
// retried for a given basket across all startups before it is skipped permanently.
const seedMaxAttempts = 5

// blockTsCacheMax is the maximum number of block timestamps held in the
// bounded FIFO cache. Prevents unbounded memory growth during historical scans.
const blockTsCacheMax = 4096

// reconnectBaseDelay is the starting delay for WebSocket reconnection backoff.
const reconnectBaseDelay = 1 * time.Second

// reconnectMaxDelay is the ceiling for WebSocket reconnection backoff.
const reconnectMaxDelay = 5 * time.Minute

var (
	topicBasketCreated = eventTopic("BasketCreated(address,address,address,string,string,string,address[],uint256[],bool)")
	topicDeposited     = eventTopic("Deposited(address,uint256,uint256,uint256)")
	topicRedeemed      = eventTopic("Redeemed(address,uint256,uint256,uint256)")
	topicRebalanced    = eventTopic("Rebalanced(address)")
	topicFeeSnapshoted = eventTopic("RevenueSnapshoted(uint256,uint256,uint256)")
	topicAssetAdded    = eventTopic("AssetAdded(address,string,string,string,address)")
	topicAssetDeact    = eventTopic("AssetDeactivated(address)")
	topicBasketSuspend = eventTopic("Suspended()")
)

var (
	depositedABI     abi.Arguments
	redeemedABI      abi.Arguments
	feeSnapshotABI   abi.Arguments
	basketCreatedABI abi.Arguments
	assetAddedABI    abi.Arguments
)

var (
	getSupportedAssetsABI abi.ABI
	getAllBasketsABI       abi.ABI
	basketStateABI        abi.ABI
	basketMetaABI         abi.ABI
)

func init() {
	// All ABI parsing failures panic at startup rather than silently producing
	// zero-value ABI objects that cause misleading errors at runtime.
	var err error

	getSupportedAssetsABI, err = abi.JSON(strings.NewReader(`[{
		"inputs": [],
		"name": "getSupportedAssets",
		"outputs": [{
			"components": [
				{"internalType":"address","name":"tokenAddress","type":"address"},
				{"internalType":"address","name":"oracle",      "type":"address"},
				{"internalType":"string", "name":"symbol",     "type":"string"},
				{"internalType":"string", "name":"name",       "type":"string"},
				{"internalType":"string", "name":"sector",     "type":"string"},
				{"internalType":"bool",   "name":"active",     "type":"bool"}
			],
			"internalType":"struct IWeaveRegistry.AssetConfig[]",
			"name":"",
			"type":"tuple[]"
		}],
		"stateMutability":"view",
		"type":"function"
	}]`))
	if err != nil {
		panic(fmt.Sprintf("indexer: parse getSupportedAssetsABI: %v", err))
	}

	getAllBasketsABI, err = abi.JSON(strings.NewReader(`[{
		"inputs": [],
		"name": "getAllBaskets",
		"outputs": [{
			"components": [
				{"internalType":"address","name":"basket",      "type":"address"},
				{"internalType":"address","name":"creatorToken","type":"address"},
				{"internalType":"address","name":"creator",     "type":"address"},
				{"internalType":"bool",   "name":"active",      "type":"bool"},
				{"internalType":"uint256","name":"createdAt",   "type":"uint256"}
			],
			"internalType":"struct IWeaveRegistry.BasketMeta[]",
			"name":"",
			"type":"tuple[]"
		}],
		"stateMutability":"view",
		"type":"function"
	}]`))
	if err != nil {
		panic(fmt.Sprintf("indexer: parse getAllBasketsABI: %v", err))
	}

	basketStateABI, err = abi.JSON(strings.NewReader(`[{
		"inputs": [],
		"name": "basketState",
		"outputs": [
			{"internalType":"address[]","name":"constituents",      "type":"address[]"},
			{"internalType":"uint256[]","name":"targetWeights",     "type":"uint256[]"},
			{"internalType":"uint256[]","name":"currentWeights",    "type":"uint256[]"},
			{"internalType":"uint256[]","name":"balances",          "type":"uint256[]"},
			{"internalType":"uint256", "name":"totalValue",         "type":"uint256"},
			{"internalType":"uint256", "name":"nav",                "type":"uint256"},
			{"internalType":"bool",    "name":"rebalancingEnabled", "type":"bool"},
			{"internalType":"uint256", "name":"driftThresholdBps",  "type":"uint256"},
			{"internalType":"uint256", "name":"maxDrift",           "type":"uint256"}
		],
		"stateMutability":"view",
		"type":"function"
	}]`))
	if err != nil {
		panic(fmt.Sprintf("indexer: parse basketStateABI: %v", err))
	}

	basketMetaABI, err = abi.JSON(strings.NewReader(`[
		{"inputs":[],"name":"name",   "outputs":[{"internalType":"string","name":"","type":"string"}],"stateMutability":"view","type":"function"},
		{"inputs":[],"name":"symbol", "outputs":[{"internalType":"string","name":"","type":"string"}],"stateMutability":"view","type":"function"},
		{"inputs":[],"name":"thesis", "outputs":[{"internalType":"string","name":"","type":"string"}],"stateMutability":"view","type":"function"}
	]`))
	if err != nil {
		panic(fmt.Sprintf("indexer: parse basketMetaABI: %v", err))
	}

	addrType, _     := abi.NewType("address", "", nil)
	stringType, _   := abi.NewType("string", "", nil)
	boolType, _     := abi.NewType("bool", "", nil)
	uint256Type, _  := abi.NewType("uint256", "", nil)
	addrSlice, _    := abi.NewType("address[]", "", nil)
	uint256Slice, _ := abi.NewType("uint256[]", "", nil)

	basketCreatedABI = abi.Arguments{
		{Name: "name",               Type: stringType},
		{Name: "symbol",             Type: stringType},
		{Name: "thesis",             Type: stringType},
		{Name: "constituents",       Type: addrSlice},
		{Name: "targetWeightsBps",   Type: uint256Slice},
		{Name: "rebalancingEnabled", Type: boolType},
	}

	assetAddedABI = abi.Arguments{
		{Name: "symbol", Type: stringType},
		{Name: "name",   Type: stringType},
		{Name: "sector", Type: stringType},
		{Name: "oracle", Type: addrType},
	}

	// Deposited(address indexed investor, uint256 usdgAmount, uint256 basketTokensMinted, uint256 feeUsdg)
	// investor is Topics[1]; the three uint256s are in Data.
	depositedABI = abi.Arguments{
		{Name: "usdgAmount",         Type: uint256Type},
		{Name: "basketTokensMinted", Type: uint256Type},
		{Name: "feeUsdg",            Type: uint256Type},
	}

	// Redeemed(address indexed investor, uint256 basketTokensBurned, uint256 usdgReturned, uint256 feeUsdg)
	redeemedABI = abi.Arguments{
		{Name: "basketTokensBurned", Type: uint256Type},
		{Name: "usdgReturned",       Type: uint256Type},
		{Name: "feeUsdg",            Type: uint256Type},
	}

	// RevenueSnapshoted(uint256 indexed snapshotId, uint256 usdgAmount, uint256 totalSupply)
	// snapshotId is Topics[1]; usdgAmount and totalSupply are in Data.
	feeSnapshotABI = abi.Arguments{
		{Name: "usdgAmount",  Type: uint256Type},
		{Name: "totalSupply", Type: uint256Type},
	}
}

// blockTsEntry is a single slot in the bounded FIFO timestamp cache.
type blockTsEntry struct {
	blockNumber uint64
	timestamp   uint64
}

// Indexer subscribes to on-chain events and writes them to SQLite.
type Indexer struct {
	ctx          context.Context
	wsURL        string
	rpcURL       string
	factoryAddr  common.Address
	registryAddr common.Address
	deployBlock  int64
	db           *db.DB

	mu          sync.RWMutex
	basketAddrs map[common.Address]bool

	// blockTs is a bounded FIFO cache of block number → Unix timestamp.
	// Protected by blockTsMu.
	blockTsMu   sync.Mutex
	blockTs     map[uint64]uint64
	blockTsFIFO []blockTsEntry
}

// New constructs an Indexer. BASKET_FACTORY_ADDRESS is validated at
// construction time so a missing environment variable fails loudly at startup.
func New(
	ctx context.Context,
	wsURL, rpcURL, registryAddr string,
	deployBlock int64,
	database *db.DB,
) (*Indexer, error) {
	factoryAddrStr := strings.TrimSpace(getEnv("BASKET_FACTORY_ADDRESS", ""))
	if factoryAddrStr == "" {
		return nil, fmt.Errorf("indexer: BASKET_FACTORY_ADDRESS environment variable is not set")
	}

	return &Indexer{
		ctx:          ctx,
		wsURL:        wsURL,
		rpcURL:       rpcURL,
		registryAddr: common.HexToAddress(registryAddr),
		factoryAddr:  common.HexToAddress(factoryAddrStr),
		deployBlock:  deployBlock,
		db:           database,
		basketAddrs:  make(map[common.Address]bool),
		blockTs:      make(map[uint64]uint64, blockTsCacheMax),
		blockTsFIFO:  make([]blockTsEntry, 0, blockTsCacheMax),
	}, nil
}

// Run starts the indexer. It syncs chain state, then launches the historical
// scan and live subscription concurrently so no events are missed during catchup.
// Duplicate events from the overlap window are discarded by UNIQUE(tx_hash, log_index).
// Run blocks until ctx is cancelled.
func (idx *Indexer) Run() {
	idx.syncFromChain()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		idx.scanTransactionalEvents()
	}()

	delay := reconnectBaseDelay
	for {
		select {
		case <-idx.ctx.Done():
			wg.Wait()
			return
		default:
		}

		if err := idx.subscribe(); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				wg.Wait()
				return
			}
			log.Printf("indexer: subscription error: %v — reconnecting in %s", err, delay)
			select {
			case <-idx.ctx.Done():
				wg.Wait()
				return
			case <-time.After(delay):
			}
			delay = minDuration(delay*2, reconnectMaxDelay)
		} else {
			wg.Wait()
			return
		}
	}
}

func (idx *Indexer) syncFromChain() {
	client, err := idx.newHTTPClient()
	if err != nil {
		log.Printf("indexer: syncFromChain dial error: %v", err)
		return
	}
	defer client.Close()

	idx.syncAssets(client)
	idx.syncBaskets(client)
	idx.seedMissingConstituents()
}

func (idx *Indexer) syncAssets(client *ethclient.Client) {
	ctx, cancel := context.WithTimeout(idx.ctx, rpcTimeout)
	defer cancel()

	data, err := client.CallContract(ctx, ethereum.CallMsg{
		To:   &idx.registryAddr,
		Data: getSupportedAssetsABI.Methods["getSupportedAssets"].ID,
	}, nil)
	if err != nil {
		log.Printf("indexer: getSupportedAssets call error: %v", err)
		return
	}

	unpacked, err := getSupportedAssetsABI.Methods["getSupportedAssets"].Outputs.Unpack(data)
	if err != nil || len(unpacked) == 0 {
		log.Printf("indexer: getSupportedAssets unpack error: %v", err)
		return
	}

	rv := reflect.ValueOf(unpacked[0])
	if rv.Kind() != reflect.Slice {
		log.Printf("indexer: syncAssets: expected slice, got %s", rv.Kind())
		return
	}

	tx, err := idx.db.Begin()
	if err != nil {
		log.Printf("indexer: syncAssets begin tx: %v", err)
		return
	}

	now := time.Now().Unix()
	count := 0

	for i := 0; i < rv.Len(); i++ {
		elem := rv.Index(i)
		if elem.Kind() == reflect.Ptr {
			elem = elem.Elem()
		}

		tokenField  := elem.FieldByName("TokenAddress")
		oracleField := elem.FieldByName("Oracle")
		symbolField := elem.FieldByName("Symbol")
		nameField   := elem.FieldByName("Name")
		sectorField := elem.FieldByName("Sector")
		activeField := elem.FieldByName("Active")

		if !tokenField.IsValid() || !oracleField.IsValid() || !symbolField.IsValid() {
			log.Printf("indexer: syncAssets: element %d missing expected fields, skipping", i)
			continue
		}

		token  := strings.ToLower(tokenField.Interface().(common.Address).Hex())
		oracle := strings.ToLower(oracleField.Interface().(common.Address).Hex())
		symbol := symbolField.String()
		name   := nameField.String()
		sector := sectorField.String()
		active := boolToInt(activeField.Bool())

		_, err := tx.Exec(`
			INSERT INTO supported_assets (address, symbol, name, sector, oracle_address, is_active, added_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(address) DO UPDATE SET
				symbol         = excluded.symbol,
				name           = excluded.name,
				sector         = excluded.sector,
				oracle_address = excluded.oracle_address,
				is_active      = excluded.is_active`,
			token, symbol, name, sector, oracle, active, now,
		)
		if err != nil {
			tx.Rollback()
			log.Printf("indexer: syncAssets upsert %s: %v — rolling back", token, err)
			return
		}
		count++
	}

	if err := tx.Commit(); err != nil {
		log.Printf("indexer: syncAssets commit: %v", err)
		return
	}

	log.Printf("indexer: synced %d assets from chain", count)
}

func (idx *Indexer) syncBaskets(client *ethclient.Client) {
	ctx, cancel := context.WithTimeout(idx.ctx, rpcTimeout)
	defer cancel()

	data, err := client.CallContract(ctx, ethereum.CallMsg{
		To:   &idx.registryAddr,
		Data: getAllBasketsABI.Methods["getAllBaskets"].ID,
	}, nil)
	if err != nil {
		log.Printf("indexer: getAllBaskets call error: %v", err)
		return
	}

	unpacked, err := getAllBasketsABI.Methods["getAllBaskets"].Outputs.Unpack(data)
	if err != nil || len(unpacked) == 0 {
		log.Printf("indexer: getAllBaskets unpack error: %v", err)
		return
	}

	rv := reflect.ValueOf(unpacked[0])
	if rv.Kind() != reflect.Slice {
		log.Printf("indexer: syncBaskets: expected slice, got %s", rv.Kind())
		return
	}

	tx, err := idx.db.Begin()
	if err != nil {
		log.Printf("indexer: syncBaskets begin tx: %v", err)
		return
	}

	count := 0

	for i := 0; i < rv.Len(); i++ {
		elem := rv.Index(i)
		if elem.Kind() == reflect.Ptr {
			elem = elem.Elem()
		}

		basketField       := elem.FieldByName("Basket")
		creatorTokenField := elem.FieldByName("CreatorToken")
		creatorField      := elem.FieldByName("Creator")
		activeField       := elem.FieldByName("Active")
		createdAtField    := elem.FieldByName("CreatedAt")

		if !basketField.IsValid() || !creatorField.IsValid() || !activeField.IsValid() {
			log.Printf("indexer: syncBaskets: element %d missing expected fields, skipping", i)
			continue
		}

		basket       := strings.ToLower(basketField.Interface().(common.Address).Hex())
		creatorToken := strings.ToLower(creatorTokenField.Interface().(common.Address).Hex())
		creator      := strings.ToLower(creatorField.Interface().(common.Address).Hex())
		createdAt    := createdAtField.Interface().(*big.Int).Int64()
		suspended    := 0
		if !activeField.Bool() {
			suspended = 1
		}

		_, err := tx.Exec(`
			INSERT INTO baskets
				(address, creator_token_address, creator_address, name, symbol, thesis,
				 rebalancing_enabled, created_at, created_tx, suspended)
			VALUES (?, ?, ?, '', '', '', 0, ?, '', ?)
			ON CONFLICT(address) DO UPDATE SET
				suspended = excluded.suspended`,
			basket, creatorToken, creator, createdAt, suspended,
		)
		if err != nil {
			tx.Rollback()
			log.Printf("indexer: syncBaskets upsert %s: %v — rolling back", basket, err)
			return
		}

		idx.mu.Lock()
		idx.basketAddrs[common.HexToAddress(basket)] = true
		idx.mu.Unlock()
		count++
	}

	if err := tx.Commit(); err != nil {
		log.Printf("indexer: syncBaskets commit: %v", err)
		return
	}

	log.Printf("indexer: synced %d baskets from chain", count)
}

// seedMissingConstituents fills metadata and constituent rows for any basket
// that has zero constituent rows and has not exceeded seedMaxAttempts failures.
// Runs once per startup — not an infinite retry loop.
func (idx *Indexer) seedMissingConstituents() {
	rows, err := idx.db.Query(`
		SELECT b.address
		FROM baskets b
		WHERE NOT EXISTS (
			SELECT 1 FROM basket_constituents bc WHERE bc.basket_address = b.address
		)
		AND NOT EXISTS (
			SELECT 1 FROM basket_seed_failures f
			WHERE f.basket_address = b.address AND f.attempts >= ?
		)`, seedMaxAttempts)
	if err != nil {
		log.Printf("indexer: seedMissingConstituents query error: %v", err)
		return
	}

	var needsSeeding []string
	for rows.Next() {
		var addr string
		if err := rows.Scan(&addr); err != nil {
			log.Printf("indexer: seedMissingConstituents scan: %v", err)
			continue
		}
		needsSeeding = append(needsSeeding, addr)
	}
	rows.Close()

	if len(needsSeeding) == 0 {
		log.Printf("indexer: no baskets require constituent seeding")
		return
	}

	log.Printf("indexer: seeding %d baskets", len(needsSeeding))

	const workers = 5
	work := make(chan string, len(needsSeeding))
	for _, addr := range needsSeeding {
		work <- addr
	}
	close(work)

	var wg sync.WaitGroup
	for i := 0; i < workers && i < len(needsSeeding); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			client, err := idx.newHTTPClient()
			if err != nil {
				log.Printf("indexer: seed worker dial error: %v", err)
				return
			}
			defer client.Close()
			for basketAddr := range work {
				if err := idx.seedOneBasket(client, basketAddr); err != nil {
					log.Printf("indexer: seedOneBasket(%s) failed: %v", basketAddr, err)
					idx.recordSeedFailure(basketAddr)
				}
			}
		}()
	}
	wg.Wait()

	log.Printf("indexer: constituent seeding complete")
}

func (idx *Indexer) recordSeedFailure(basketAddr string) {
	_, err := idx.db.Exec(`
		INSERT INTO basket_seed_failures (basket_address, attempts, last_attempt)
		VALUES (?, 1, ?)
		ON CONFLICT(basket_address) DO UPDATE SET
			attempts     = attempts + 1,
			last_attempt = excluded.last_attempt`,
		basketAddr, time.Now().Unix(),
	)
	if err != nil {
		log.Printf("indexer: recordSeedFailure(%s): %v", basketAddr, err)
	}
}

// seedOneBasket fetches basketState() plus name/symbol/thesis and writes all
// constituent rows and basket metadata atomically. Returns an error on any
// RPC or database failure so the caller can record the failure.
func (idx *Indexer) seedOneBasket(client *ethclient.Client, basketAddr string) error {
	addr := common.HexToAddress(basketAddr)

	ctx, cancel := context.WithTimeout(idx.ctx, rpcTimeout)
	defer cancel()

	data, err := client.CallContract(ctx, ethereum.CallMsg{
		To:   &addr,
		Data: basketStateABI.Methods["basketState"].ID,
	}, nil)
	if err != nil {
		return fmt.Errorf("basketState RPC: %w", err)
	}

	unpacked, err := basketStateABI.Methods["basketState"].Outputs.Unpack(data)
	if err != nil || len(unpacked) < 9 {
		return fmt.Errorf("basketState unpack: %w", err)
	}

	constituents, ok1     := unpacked[0].([]common.Address)
	targetWeights, ok2    := unpacked[1].([]*big.Int)
	rebalancingEnabled, _ := unpacked[6].(bool)
	driftThresholdBps, _  := unpacked[7].(*big.Int)

	if !ok1 || !ok2 || len(constituents) == 0 {
		return fmt.Errorf("basketState: unexpected type or empty constituents")
	}

	callMeta := func(method string) (string, error) {
		mctx, mcancel := context.WithTimeout(idx.ctx, rpcTimeout)
		defer mcancel()
		d, err := client.CallContract(mctx, ethereum.CallMsg{
			To:   &addr,
			Data: basketMetaABI.Methods[method].ID,
		}, nil)
		if err != nil {
			return "", fmt.Errorf("%s RPC: %w", method, err)
		}
		out, err := basketMetaABI.Methods[method].Outputs.Unpack(d)
		if err != nil || len(out) == 0 {
			return "", fmt.Errorf("%s unpack: %w", method, err)
		}
		s, ok := out[0].(string)
		if !ok {
			return "", fmt.Errorf("%s: result is not string", method)
		}
		return s, nil
	}

	name, err := callMeta("name")
	if err != nil {
		return err
	}
	symbol, err := callMeta("symbol")
	if err != nil {
		return err
	}
	thesis, err := callMeta("thesis")
	if err != nil {
		return err
	}

	// Resolve constituent symbols before opening the write transaction to
	// avoid holding a write lock during reads.
	type constituentRow struct {
		addr   string
		symbol string
		weight int64
	}
	cRows := make([]constituentRow, 0, len(constituents))
	for i, c := range constituents {
		cAddr := strings.ToLower(c.Hex())
		var cSymbol string
		err := idx.db.QueryRow(
			`SELECT symbol FROM supported_assets WHERE address = ?`, cAddr,
		).Scan(&cSymbol)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("symbol lookup for %s: %w", cAddr, err)
		}
		if errors.Is(err, sql.ErrNoRows) {
			log.Printf("indexer: seedOneBasket(%s): constituent %s not in supported_assets — symbol will be empty", basketAddr, cAddr)
		}
		weight := int64(0)
		if i < len(targetWeights) && targetWeights[i] != nil {
			weight = targetWeights[i].Int64()
		}
		cRows = append(cRows, constituentRow{addr: cAddr, symbol: cSymbol, weight: weight})
	}

	tx, err := idx.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}

	if driftThresholdBps != nil {
		_, err = tx.Exec(`
			UPDATE baskets
			SET name=?, symbol=?, thesis=?, rebalancing_enabled=?, drift_threshold_bps=?
			WHERE address=?`,
			name, symbol, thesis, boolToInt(rebalancingEnabled), driftThresholdBps.Int64(), basketAddr,
		)
	} else {
		_, err = tx.Exec(`
			UPDATE baskets
			SET name=?, symbol=?, thesis=?, rebalancing_enabled=?
			WHERE address=?`,
			name, symbol, thesis, boolToInt(rebalancingEnabled), basketAddr,
		)
	}
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("update basket meta: %w", err)
	}

	for i, row := range cRows {
		_, err := tx.Exec(`
			INSERT INTO basket_constituents
				(basket_address, stock_address, symbol, target_weight_bps, display_order)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(basket_address, stock_address) DO NOTHING`,
			basketAddr, row.addr, row.symbol, row.weight, i,
		)
		if err != nil {
			tx.Rollback()
			return fmt.Errorf("insert constituent %s: %w", row.addr, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	log.Printf("indexer: seeded %d constituents for basket %s", len(cRows), basketAddr)
	return nil
}

// scanTransactionalEvents performs a cursor-based block scan for deposits,
// redemptions, rebalances, and fee snapshots. The cursor advance and all log
// writes for each chunk are committed in the same SQLite transaction, so a
// crash mid-chunk replays that chunk on the next startup. Duplicate events
// from re-scans are discarded by UNIQUE(tx_hash, log_index).
func (idx *Indexer) scanTransactionalEvents() {
	client, err := idx.newHTTPClient()
	if err != nil {
		log.Printf("indexer: scanTransactionalEvents dial error: %v", err)
		return
	}
	defer client.Close()

	var fromBlock int64
	err = idx.db.QueryRow(
		`SELECT block_num FROM sync_cursors WHERE key = 'events'`,
	).Scan(&fromBlock)
	if err != nil {
		fromBlock = idx.deployBlock
		if _, err := idx.db.Exec(
			`INSERT INTO sync_cursors (key, block_num) VALUES ('events', ?)`, fromBlock,
		); err != nil {
			log.Printf("indexer: scanTransactionalEvents: failed to initialise cursor: %v", err)
			return
		}
	}

	hctx, hcancel := context.WithTimeout(idx.ctx, rpcTimeout)
	latestHeader, err := client.HeaderByNumber(hctx, nil)
	hcancel()
	if err != nil {
		log.Printf("indexer: scanTransactionalEvents latest block error: %v", err)
		return
	}
	toBlock := latestHeader.Number.Int64()

	if fromBlock >= toBlock {
		log.Printf("indexer: event cursor current at block %d", fromBlock)
		return
	}

	log.Printf("indexer: scanning transactional events blocks %d → %d", fromBlock, toBlock)

	addresses := idx.filterAddresses()

	for start := fromBlock; start <= toBlock; start += chunkSize {
		select {
		case <-idx.ctx.Done():
			log.Printf("indexer: scan interrupted at block %d by context cancellation", start)
			return
		default:
		}

		end := start + chunkSize - 1
		if end > toBlock {
			end = toBlock
		}

		filterCtx, filterCancel := context.WithTimeout(idx.ctx, rpcTimeout)
		logs, err := client.FilterLogs(filterCtx, ethereum.FilterQuery{
			FromBlock: big.NewInt(start),
			ToBlock:   big.NewInt(end),
			Addresses: addresses,
			Topics: [][]common.Hash{{
				topicBasketCreated,
				topicDeposited,
				topicRedeemed,
				topicRebalanced,
				topicFeeSnapshoted,
			}},
		})
		filterCancel()

		if err != nil {
			log.Printf("indexer: FilterLogs [%d-%d]: %v — stopping scan", start, end, err)
			return
		}

		if err := idx.writeChunkAtomic(client, logs, end); err != nil {
			log.Printf("indexer: writeChunkAtomic [%d-%d]: %v — stopping scan", start, end, err)
			return
		}
	}

	log.Printf("indexer: transactional event scan complete through block %d", toBlock)
}

// writeChunkAtomic writes all logs for a scan chunk and advances the cursor
// in a single SQLite transaction. BasketCreated events are handled inline
// with their own sub-transaction because they also update basketAddrs.
// Either the whole chunk commits or nothing does.
func (idx *Indexer) writeChunkAtomic(client *ethclient.Client, logs []types.Log, endBlock int64) error {
	tx, err := idx.db.Begin()
	if err != nil {
		return fmt.Errorf("begin chunk tx: %w", err)
	}

	for _, vLog := range logs {
		if len(vLog.Topics) == 0 {
			continue
		}

		if vLog.Topics[0] == topicBasketCreated {
			// BasketCreated writes to two tables and updates basketAddrs.
			// It needs its own transaction. Commit the current chunk tx
			// first so its writes are durable, handle the basket, then
			// open a fresh chunk tx for any remaining logs.
			if err := tx.Commit(); err != nil {
				return fmt.Errorf("pre-basket commit: %w", err)
			}
			idx.handleBasketCreated(client, vLog)
			tx, err = idx.db.Begin()
			if err != nil {
				return fmt.Errorf("post-basket begin tx: %w", err)
			}
			continue
		}

		if err := idx.writeLog(tx, client, vLog); err != nil {
			tx.Rollback()
			return fmt.Errorf("write log tx=%s idx=%d: %w", vLog.TxHash.Hex(), vLog.Index, err)
		}
	}

	if _, err := tx.Exec(
		`UPDATE sync_cursors SET block_num = ? WHERE key = 'events'`, endBlock,
	); err != nil {
		tx.Rollback()
		return fmt.Errorf("advance cursor to %d: %w", endBlock, err)
	}

	return tx.Commit()
}

// writeLog routes a single log to the appropriate write function within an
// open transaction. Does not handle BasketCreated — that is handled inline
// by writeChunkAtomic before this function is called.
func (idx *Indexer) writeLog(tx *sql.Tx, client *ethclient.Client, vLog types.Log) error {
	if len(vLog.Topics) == 0 {
		return nil
	}
	switch vLog.Topics[0] {
	case topicDeposited:
		return idx.writeDeposited(tx, client, vLog)
	case topicRedeemed:
		return idx.writeRedeemed(tx, client, vLog)
	case topicRebalanced:
		return idx.writeRebalanced(tx, client, vLog)
	case topicFeeSnapshoted:
		return idx.writeFeeSnapshot(tx, client, vLog)
	}
	return nil
}

// handleLog is used by the live subscription. Each event type gets its own
// transaction. Write errors are logged but do not stop the subscription loop
// since missed events are replayed on reconnection via the scan cursor.
func (idx *Indexer) handleLog(client *ethclient.Client, vLog types.Log) {
	if len(vLog.Topics) == 0 {
		return
	}
	switch vLog.Topics[0] {
	case topicBasketCreated:
		idx.handleBasketCreated(client, vLog)

	case topicDeposited:
		tx, err := idx.db.Begin()
		if err != nil {
			log.Printf("indexer: live Deposited begin tx: %v", err)
			return
		}
		if err := idx.writeDeposited(tx, client, vLog); err != nil {
			tx.Rollback()
			log.Printf("indexer: live writeDeposited: %v", err)
			return
		}
		if err := tx.Commit(); err != nil {
			log.Printf("indexer: live Deposited commit: %v", err)
		}

	case topicRedeemed:
		tx, err := idx.db.Begin()
		if err != nil {
			log.Printf("indexer: live Redeemed begin tx: %v", err)
			return
		}
		if err := idx.writeRedeemed(tx, client, vLog); err != nil {
			tx.Rollback()
			log.Printf("indexer: live writeRedeemed: %v", err)
			return
		}
		if err := tx.Commit(); err != nil {
			log.Printf("indexer: live Redeemed commit: %v", err)
		}

	case topicRebalanced:
		tx, err := idx.db.Begin()
		if err != nil {
			log.Printf("indexer: live Rebalanced begin tx: %v", err)
			return
		}
		if err := idx.writeRebalanced(tx, client, vLog); err != nil {
			tx.Rollback()
			log.Printf("indexer: live writeRebalanced: %v", err)
			return
		}
		if err := tx.Commit(); err != nil {
			log.Printf("indexer: live Rebalanced commit: %v", err)
		}

	case topicFeeSnapshoted:
		tx, err := idx.db.Begin()
		if err != nil {
			log.Printf("indexer: live FeeSnapshot begin tx: %v", err)
			return
		}
		if err := idx.writeFeeSnapshot(tx, client, vLog); err != nil {
			tx.Rollback()
			log.Printf("indexer: live writeFeeSnapshot: %v", err)
			return
		}
		if err := tx.Commit(); err != nil {
			log.Printf("indexer: live FeeSnapshot commit: %v", err)
		}

	case topicAssetAdded:
		idx.handleAssetAdded(vLog)

	case topicAssetDeact:
		idx.handleAssetDeactivated(vLog)

	case topicBasketSuspend:
		idx.handleBasketSuspended(vLog)
	}
}

// handleBasketCreated takes a client so it can resolve the block timestamp
// correctly. Storing int64(vLog.BlockNumber) as created_at would produce
// dates in year 4136 — blockTimestamp resolves the actual Unix timestamp.
func (idx *Indexer) handleBasketCreated(client *ethclient.Client, vLog types.Log) {
	if len(vLog.Topics) < 4 {
		log.Printf("indexer: handleBasketCreated: expected 4 topics, got %d — tx=%s", len(vLog.Topics), vLog.TxHash.Hex())
		return
	}

	basket       := strings.ToLower(common.HexToAddress(vLog.Topics[1].Hex()).Hex())
	creatorToken := strings.ToLower(common.HexToAddress(vLog.Topics[2].Hex()).Hex())
	creator      := strings.ToLower(common.HexToAddress(vLog.Topics[3].Hex()).Hex())

	decoded, err := basketCreatedABI.Unpack(vLog.Data)
	if err != nil || len(decoded) < 6 {
		log.Printf("indexer: handleBasketCreated decode error tx=%s: %v", vLog.TxHash.Hex(), err)
		return
	}

	name, _          := decoded[0].(string)
	symbol, _        := decoded[1].(string)
	thesis, _        := decoded[2].(string)
	constituents, _  := decoded[3].([]common.Address)
	targetWeights, _ := decoded[4].([]*big.Int)
	rebalancing, _   := decoded[5].(bool)

	type constituentRow struct {
		addr   string
		symbol string
		weight int64
	}
	cRows := make([]constituentRow, 0, len(constituents))
	for i, c := range constituents {
		cAddr := strings.ToLower(c.Hex())
		var cSymbol string
		err := idx.db.QueryRow(
			`SELECT symbol FROM supported_assets WHERE address = ?`, cAddr,
		).Scan(&cSymbol)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			log.Printf("indexer: handleBasketCreated symbol lookup %s: %v", cAddr, err)
		}
		weight := int64(0)
		if i < len(targetWeights) && targetWeights[i] != nil {
			weight = targetWeights[i].Int64()
		}
		cRows = append(cRows, constituentRow{addr: cAddr, symbol: cSymbol, weight: weight})
	}

	// Resolve the actual block timestamp — never store block number as created_at.
	createdAt := idx.blockTimestamp(client, vLog.BlockNumber)

	tx, err := idx.db.Begin()
	if err != nil {
		log.Printf("indexer: handleBasketCreated begin tx: %v", err)
		return
	}

	_, err = tx.Exec(`
		INSERT INTO baskets
			(address, creator_token_address, creator_address, name, symbol, thesis,
			 rebalancing_enabled, created_at, created_tx, suspended)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0)
		ON CONFLICT(address) DO UPDATE SET
			name                = excluded.name,
			symbol              = excluded.symbol,
			thesis              = excluded.thesis,
			rebalancing_enabled = excluded.rebalancing_enabled`,
		basket, creatorToken, creator, name, symbol, thesis,
		boolToInt(rebalancing), createdAt, vLog.TxHash.Hex(),
	)
	if err != nil {
		tx.Rollback()
		log.Printf("indexer: handleBasketCreated insert basket: %v", err)
		return
	}

	for i, row := range cRows {
		_, err := tx.Exec(`
			INSERT INTO basket_constituents
				(basket_address, stock_address, symbol, target_weight_bps, display_order)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(basket_address, stock_address) DO NOTHING`,
			basket, row.addr, row.symbol, row.weight, i,
		)
		if err != nil {
			tx.Rollback()
			log.Printf("indexer: handleBasketCreated insert constituent %s: %v", row.addr, err)
			return
		}
	}

	if err := tx.Commit(); err != nil {
		log.Printf("indexer: handleBasketCreated commit: %v", err)
		return
	}

	idx.mu.Lock()
	idx.basketAddrs[common.HexToAddress(basket)] = true
	idx.mu.Unlock()

	log.Printf("indexer: basket %s indexed with %d constituents", basket, len(cRows))
}

func (idx *Indexer) handleAssetAdded(vLog types.Log) {
	if len(vLog.Topics) < 2 {
		return
	}

	token := strings.ToLower(common.HexToAddress(vLog.Topics[1].Hex()).Hex())

	decoded, err := assetAddedABI.Unpack(vLog.Data)
	if err != nil || len(decoded) < 4 {
		log.Printf("indexer: handleAssetAdded decode error %s: %v", token, err)
		return
	}

	symbol, _ := decoded[0].(string)
	name, _   := decoded[1].(string)
	sector, _ := decoded[2].(string)
	oracle, _ := decoded[3].(common.Address)

	_, err = idx.db.Exec(`
		INSERT INTO supported_assets (address, symbol, name, sector, oracle_address, is_active, added_at)
		VALUES (?, ?, ?, ?, ?, 1, ?)
		ON CONFLICT(address) DO UPDATE SET
			symbol         = excluded.symbol,
			name           = excluded.name,
			sector         = excluded.sector,
			oracle_address = excluded.oracle_address,
			is_active      = excluded.is_active`,
		token, symbol, name, sector, strings.ToLower(oracle.Hex()),
		int64(vLog.BlockNumber),
	)
	if err != nil {
		log.Printf("indexer: handleAssetAdded insert %s: %v", token, err)
	} else {
		log.Printf("indexer: asset indexed — %s (%s) oracle=%s", symbol, token, oracle.Hex())
	}
}

func (idx *Indexer) writeDeposited(tx *sql.Tx, client *ethclient.Client, vLog types.Log) error {
	if len(vLog.Topics) < 2 {
		return nil
	}

	decoded, err := depositedABI.Unpack(vLog.Data)
	if err != nil || len(decoded) < 3 {
		return fmt.Errorf("Deposited unpack tx=%s: %w", vLog.TxHash.Hex(), err)
	}

	investor     := strings.ToLower(common.HexToAddress(vLog.Topics[1].Hex()).Hex())
	basket       := strings.ToLower(vLog.Address.Hex())
	usdgAmount   := decoded[0].(*big.Int)
	tokensMinted := decoded[1].(*big.Int)
	feeUsdg      := decoded[2].(*big.Int)
	ts           := idx.blockTimestamp(client, vLog.BlockNumber)

	_, err = tx.Exec(`
		INSERT INTO deposits
			(basket_address, investor_address, usdg_amount, basket_tokens_minted, fee_usdg, timestamp, tx_hash, log_index)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(tx_hash, log_index) DO NOTHING`,
		basket, investor,
		usdgAmount.String(), tokensMinted.String(), feeUsdg.String(),
		ts, vLog.TxHash.Hex(), vLog.Index,
	)
	return err
}

func (idx *Indexer) writeRedeemed(tx *sql.Tx, client *ethclient.Client, vLog types.Log) error {
	if len(vLog.Topics) < 2 {
		return nil
	}

	decoded, err := redeemedABI.Unpack(vLog.Data)
	if err != nil || len(decoded) < 3 {
		return fmt.Errorf("Redeemed unpack tx=%s: %w", vLog.TxHash.Hex(), err)
	}

	investor     := strings.ToLower(common.HexToAddress(vLog.Topics[1].Hex()).Hex())
	basket       := strings.ToLower(vLog.Address.Hex())
	tokensBurned := decoded[0].(*big.Int)
	usdgReturned := decoded[1].(*big.Int)
	feeUsdg      := decoded[2].(*big.Int)
	ts           := idx.blockTimestamp(client, vLog.BlockNumber)

	_, err = tx.Exec(`
		INSERT INTO redemptions
			(basket_address, investor_address, basket_tokens_burned, usdg_returned, fee_usdg, timestamp, tx_hash, log_index)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(tx_hash, log_index) DO NOTHING`,
		basket, investor,
		tokensBurned.String(), usdgReturned.String(), feeUsdg.String(),
		ts, vLog.TxHash.Hex(), vLog.Index,
	)
	return err
}

func (idx *Indexer) writeRebalanced(tx *sql.Tx, client *ethclient.Client, vLog types.Log) error {
	if len(vLog.Topics) < 2 {
		return nil
	}

	triggeredBy := strings.ToLower(common.HexToAddress(vLog.Topics[1].Hex()).Hex())
	basket      := strings.ToLower(vLog.Address.Hex())
	ts          := idx.blockTimestamp(client, vLog.BlockNumber)

	_, err := tx.Exec(`
		INSERT INTO rebalances (basket_address, triggered_by, timestamp, tx_hash, log_index)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(tx_hash, log_index) DO NOTHING`,
		basket, triggeredBy, ts, vLog.TxHash.Hex(), vLog.Index,
	)
	return err
}

func (idx *Indexer) writeFeeSnapshot(tx *sql.Tx, client *ethclient.Client, vLog types.Log) error {
	if len(vLog.Topics) < 2 {
		return nil
	}

	// snapshotId is the first indexed parameter — Topics[1].
	// usdgAmount is the first non-indexed parameter — first word of Data.
	snapshotID := new(big.Int).SetBytes(vLog.Topics[1].Bytes()).Int64()

	decoded, err := feeSnapshotABI.Unpack(vLog.Data)
	if err != nil || len(decoded) < 1 {
		return fmt.Errorf("RevenueSnapshoted unpack tx=%s: %w", vLog.TxHash.Hex(), err)
	}

	basket     := strings.ToLower(vLog.Address.Hex())
	usdgAmount := decoded[0].(*big.Int)
	ts         := idx.blockTimestamp(client, vLog.BlockNumber)

	_, err = tx.Exec(`
		INSERT INTO fee_snapshots (basket_address, snapshot_id, usdg_amount, timestamp, tx_hash, log_index)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(tx_hash, log_index) DO NOTHING`,
		basket, snapshotID, usdgAmount.String(), ts, vLog.TxHash.Hex(), vLog.Index,
	)
	return err
}

func (idx *Indexer) handleAssetDeactivated(vLog types.Log) {
	if len(vLog.Topics) < 2 {
		return
	}
	token := strings.ToLower(common.HexToAddress(vLog.Topics[1].Hex()).Hex())
	if _, err := idx.db.Exec(
		`UPDATE supported_assets SET is_active = 0 WHERE address = ?`, token,
	); err != nil {
		log.Printf("indexer: deactivate asset %s: %v", token, err)
	}
}

func (idx *Indexer) handleBasketSuspended(vLog types.Log) {
	basket := strings.ToLower(vLog.Address.Hex())
	if _, err := idx.db.Exec(
		`UPDATE baskets SET suspended = 1 WHERE address = ?`, basket,
	); err != nil {
		log.Printf("indexer: suspend basket %s: %v", basket, err)
	}
}

// blockTimestamp returns the Unix timestamp for a block number from a bounded
// FIFO cache, fetching via RPC on a miss.
func (idx *Indexer) blockTimestamp(client *ethclient.Client, blockNumber uint64) int64 {
	idx.blockTsMu.Lock()
	if ts, ok := idx.blockTs[blockNumber]; ok {
		idx.blockTsMu.Unlock()
		return int64(ts)
	}
	idx.blockTsMu.Unlock()

	if client == nil {
		log.Printf("indexer: blockTimestamp(%d): nil client — storing 0", blockNumber)
		return 0
	}

	ctx, cancel := context.WithTimeout(idx.ctx, rpcTimeout)
	defer cancel()

	header, err := client.HeaderByNumber(ctx, new(big.Int).SetUint64(blockNumber))
	if err != nil {
		log.Printf("indexer: blockTimestamp(%d): %v — storing 0", blockNumber, err)
		return 0
	}

	ts := header.Time

	idx.blockTsMu.Lock()
	defer idx.blockTsMu.Unlock()

	if len(idx.blockTsFIFO) >= blockTsCacheMax {
		oldest := idx.blockTsFIFO[0]
		idx.blockTsFIFO = idx.blockTsFIFO[1:]
		delete(idx.blockTs, oldest.blockNumber)
	}

	idx.blockTs[blockNumber] = ts
	idx.blockTsFIFO = append(idx.blockTsFIFO, blockTsEntry{blockNumber: blockNumber, timestamp: ts})

	return int64(ts)
}

// subscribe opens a WebSocket connection and processes live events until
// ctx is cancelled or the connection drops. Events are drained in a separate
// goroutine so the receive loop never blocks on handler RPC calls.
func (idx *Indexer) subscribe() error {
	dialCtx, dialCancel := context.WithTimeout(idx.ctx, rpcTimeout)
	defer dialCancel()

	client, err := ethclient.DialContext(dialCtx, idx.wsURL)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer client.Close()

	query := ethereum.FilterQuery{Addresses: idx.filterAddresses()}

	// Buffer 512 events. The drain goroutine processes them concurrently
	// with the receive loop so back-pressure from slow RPC calls in
	// blockTimestamp does not cause the WebSocket library to drop events.
	logs := make(chan types.Log, 512)
	sub, err := client.SubscribeFilterLogs(idx.ctx, query, logs)
	if err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}
	defer sub.Unsubscribe()

	// Drain the log channel in a separate goroutine. handleLog makes
	// blocking RPC calls; running it off the receive loop prevents
	// channel saturation under load.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for vLog := range logs {
			idx.handleLog(client, vLog)
		}
	}()

	log.Println("indexer: live subscription active")

	select {
	case <-idx.ctx.Done():
		return nil
	case err := <-sub.Err():
		return fmt.Errorf("subscription: %w", err)
	}
}

// filterAddresses returns the registry, factory, and all known basket proxy
// addresses to include in a FilterQuery. Called fresh on each subscribe()
// invocation so newly indexed baskets are included on reconnection.
func (idx *Indexer) filterAddresses() []common.Address {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	addresses := make([]common.Address, 0, len(idx.basketAddrs)+2)
	addresses = append(addresses, idx.registryAddr)
	addresses = append(addresses, idx.factoryAddr)
	for addr := range idx.basketAddrs {
		addresses = append(addresses, addr)
	}
	return addresses
}

func (idx *Indexer) newHTTPClient() (*ethclient.Client, error) {
	ctx, cancel := context.WithTimeout(idx.ctx, rpcTimeout)
	defer cancel()
	return ethclient.DialContext(ctx, idx.rpcURL)
}

func eventTopic(sig string) common.Hash {
	return crypto.Keccak256Hash([]byte(sig))
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// minDuration returns the smaller of two durations.
// Named minDuration to avoid shadowing the Go 1.21 builtin min.
func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

// getEnv returns the environment variable value or a fallback.
func getEnv(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}