package indexer

import (
	"context"
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

var basketCreatedABI abi.Arguments
var assetAddedABI abi.Arguments

var getSupportedAssetsABI, _ = abi.JSON(strings.NewReader(`[{
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

var getAllBasketsABI, _ = abi.JSON(strings.NewReader(`[{
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

var basketStateABI, _ = abi.JSON(strings.NewReader(`[{
	"inputs": [],
	"name": "basketState",
	"outputs": [
		{"internalType":"address[]","name":"constituents",       "type":"address[]"},
		{"internalType":"uint256[]","name":"targetWeights",      "type":"uint256[]"},
		{"internalType":"uint256[]","name":"currentWeights",     "type":"uint256[]"},
		{"internalType":"uint256[]","name":"balances",           "type":"uint256[]"},
		{"internalType":"uint256", "name":"totalValue",          "type":"uint256"},
		{"internalType":"uint256", "name":"nav",                 "type":"uint256"},
		{"internalType":"bool",    "name":"rebalancingEnabled",  "type":"bool"},
		{"internalType":"uint256", "name":"driftThresholdBps",   "type":"uint256"},
		{"internalType":"uint256", "name":"maxDrift",            "type":"uint256"}
	],
	"stateMutability":"view",
	"type":"function"
}]`))

var basketMetaABI, _ = abi.JSON(strings.NewReader(`[
	{"inputs":[],"name":"name",   "outputs":[{"internalType":"string","name":"","type":"string"}],"stateMutability":"view","type":"function"},
	{"inputs":[],"name":"symbol", "outputs":[{"internalType":"string","name":"","type":"string"}],"stateMutability":"view","type":"function"},
	{"inputs":[],"name":"thesis", "outputs":[{"internalType":"string","name":"","type":"string"}],"stateMutability":"view","type":"function"}
]`))

func init() {
	addrType,     _ := abi.NewType("address",   "", nil)
	stringType,   _ := abi.NewType("string",    "", nil)
	boolType,     _ := abi.NewType("bool",      "", nil)
	addrSlice,    _ := abi.NewType("address[]", "", nil)
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
}

type Indexer struct {
	ctx          context.Context
	wsURL        string
	rpcURL       string
	registryAddr common.Address
	deployBlock  int64
	db           *db.DB

	mu          sync.RWMutex
	basketAddrs map[common.Address]bool
}

func New(ctx context.Context, wsURL, rpcURL, registryAddr string, deployBlock int64, database *db.DB) (*Indexer, error) {
	return &Indexer{
		ctx:          ctx,
		wsURL:        wsURL,
		rpcURL:       rpcURL,
		registryAddr: common.HexToAddress(registryAddr),
		deployBlock:  deployBlock,
		db:           database,
		basketAddrs:  make(map[common.Address]bool),
	}, nil
}

func (idx *Indexer) Run() {
	idx.syncFromChain()
	idx.scanTransactionalEvents()

	for {
		select {
		case <-idx.ctx.Done():
			return
		default:
		}
		if err := idx.subscribe(); err != nil {
			log.Printf("indexer: subscription error: %v — reconnecting in 5s", err)
			time.Sleep(5 * time.Second)
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
	data, err := client.CallContract(idx.ctx, ethereum.CallMsg{
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

	// Use reflect to iterate the returned slice regardless of the
	// runtime-generated anonymous struct type that ABI produces.
	rv := reflect.ValueOf(unpacked[0])
	if rv.Kind() != reflect.Slice {
		log.Printf("indexer: syncAssets: expected slice, got %s", rv.Kind())
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

		if !tokenField.IsValid() || !oracleField.IsValid() {
			continue
		}

		token  := strings.ToLower(tokenField.Interface().(common.Address).Hex())
		oracle := strings.ToLower(oracleField.Interface().(common.Address).Hex())
		symbol := symbolField.String()
		name   := nameField.String()
		sector := sectorField.String()
		active := boolToInt(activeField.Bool())

		_, err := idx.db.Exec(`
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
			log.Printf("indexer: syncAssets upsert %s: %v", token, err)
		} else {
			count++
		}
	}

	log.Printf("indexer: synced %d assets from chain", count)
}

func (idx *Indexer) syncBaskets(client *ethclient.Client) {
	data, err := client.CallContract(idx.ctx, ethereum.CallMsg{
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

	count := 0

	for i := 0; i < rv.Len(); i++ {
		elem := rv.Index(i)
		if elem.Kind() == reflect.Ptr {
			elem = elem.Elem()
		}

		basketField       := elem.FieldByName("Basket")
		creatorTokenField := elem.FieldByName("CreatorToken")
		creatorField      := elem.FieldByName("Creator")
		createdAtField    := elem.FieldByName("CreatedAt")

		if !basketField.IsValid() || !creatorField.IsValid() {
			continue
		}

		basket       := strings.ToLower(basketField.Interface().(common.Address).Hex())
		creatorToken := strings.ToLower(creatorTokenField.Interface().(common.Address).Hex())
		creator      := strings.ToLower(creatorField.Interface().(common.Address).Hex())
		createdAt    := createdAtField.Interface().(*big.Int).Int64()

		_, err := idx.db.Exec(`
			INSERT INTO baskets
				(address, creator_token_address, creator_address, name, symbol, thesis,
				 rebalancing_enabled, created_at, created_tx, suspended)
			VALUES (?, ?, ?, '', '', '', 0, ?, '', 0)
			ON CONFLICT(address) DO NOTHING`,
			basket, creatorToken, creator, createdAt,
		)
		if err != nil {
			log.Printf("indexer: syncBaskets upsert %s: %v", basket, err)
			continue
		}

		idx.mu.Lock()
		idx.basketAddrs[common.HexToAddress(basket)] = true
		idx.mu.Unlock()
		count++
	}

	log.Printf("indexer: synced %d baskets from chain", count)
}

// seedMissingConstituents fills constituent rows and basket metadata for any
// basket that has zero constituent rows. Uses a paginated loop — never more
// than 50 addresses in memory at once. Since successfully seeded baskets drop
// out of the WHERE NOT EXISTS condition, the query always uses OFFSET 0 and
// the result set shrinks naturally as work completes.
func (idx *Indexer) seedMissingConstituents() {
	const pageSize = 50
	const workers  = 5

	for {
		rows, err := idx.db.Query(`
			SELECT b.address FROM baskets b
			WHERE NOT EXISTS (
				SELECT 1 FROM basket_constituents bc WHERE bc.basket_address = b.address
			)
			LIMIT ?`, pageSize)
		if err != nil {
			log.Printf("indexer: seedMissingConstituents query error: %v", err)
			return
		}

		var page []string
		for rows.Next() {
			var addr string
			if rows.Scan(&addr) == nil {
				page = append(page, addr)
			}
		}
		rows.Close()

		if len(page) == 0 {
			break
		}

		log.Printf("indexer: seeding page of %d baskets", len(page))

		work := make(chan string, len(page))
		for _, addr := range page {
			work <- addr
		}
		close(work)

		var wg sync.WaitGroup
		for i := 0; i < workers; i++ {
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
					idx.seedOneBasket(client, basketAddr)
				}
			}()
		}
		wg.Wait()
	}

	log.Printf("indexer: constituent seeding complete")
}

// seedOneBasket calls basketState() once plus name/symbol/thesis — 4 RPC calls total.
// rebalancingEnabled and driftThresholdBps come from basketState() output directly,
// eliminating the two separate calls the previous version made.
func (idx *Indexer) seedOneBasket(client *ethclient.Client, basketAddr string) {
	addr := common.HexToAddress(basketAddr)

	// Single basketState() call yields constituents, weights, rebalancingEnabled, driftThresholdBps.
	data, err := client.CallContract(idx.ctx, ethereum.CallMsg{
		To:   &addr,
		Data: basketStateABI.Methods["basketState"].ID,
	}, nil)
	if err != nil {
		log.Printf("indexer: seedOneBasket basketState(%s): %v", basketAddr, err)
		return
	}

	unpacked, err := basketStateABI.Methods["basketState"].Outputs.Unpack(data)
	if err != nil || len(unpacked) < 9 {
		log.Printf("indexer: seedOneBasket unpack(%s): %v", basketAddr, err)
		return
	}

	constituents,     ok1 := unpacked[0].([]common.Address)
	targetWeights,    ok2 := unpacked[1].([]*big.Int)
	rebalancingEnabled,_  := unpacked[6].(bool)
	driftThresholdBps,_   := unpacked[7].(*big.Int)

	if !ok1 || !ok2 || len(constituents) == 0 {
		log.Printf("indexer: seedOneBasket(%s): unexpected constituent types", basketAddr)
		return
	}

	// Remaining 3 RPC calls: name, symbol, thesis.
	callMeta := func(method string) string {
		d, err := client.CallContract(idx.ctx, ethereum.CallMsg{
			To:   &addr,
			Data: basketMetaABI.Methods[method].ID,
		}, nil)
		if err != nil {
			return ""
		}
		out, err := basketMetaABI.Methods[method].Outputs.Unpack(d)
		if err != nil || len(out) == 0 {
			return ""
		}
		s, _ := out[0].(string)
		return s
	}

	name   := callMeta("name")
	symbol := callMeta("symbol")
	thesis := callMeta("thesis")

	// Look up constituent symbols from supported_assets BEFORE opening the
	// write transaction — reading the DB inside an open write transaction
	// causes lock contention in SQLite under Go's database/sql.
	type constituentRow struct {
		addr   string
		symbol string
		weight int64
	}
	rows := make([]constituentRow, 0, len(constituents))
	for i, c := range constituents {
		cAddr := strings.ToLower(c.Hex())
		var cSymbol string
		idx.db.QueryRow(
			`SELECT symbol FROM supported_assets WHERE address = ?`, cAddr,
		).Scan(&cSymbol)
		weight := int64(0)
		if i < len(targetWeights) && targetWeights[i] != nil {
			weight = targetWeights[i].Int64()
		}
		rows = append(rows, constituentRow{addr: cAddr, symbol: cSymbol, weight: weight})
	}

	// Write metadata update and all constituent rows atomically.
	tx, err := idx.db.Begin()
	if err != nil {
		log.Printf("indexer: seedOneBasket begin tx(%s): %v", basketAddr, err)
		return
	}

	if driftThresholdBps != nil {
		_, err = tx.Exec(`
			UPDATE baskets SET name=?, symbol=?, thesis=?, rebalancing_enabled=?, drift_threshold_bps=?
			WHERE address=?`,
			name, symbol, thesis, boolToInt(rebalancingEnabled), driftThresholdBps.Int64(), basketAddr,
		)
	} else {
		_, err = tx.Exec(`
			UPDATE baskets SET name=?, symbol=?, thesis=?, rebalancing_enabled=?
			WHERE address=?`,
			name, symbol, thesis, boolToInt(rebalancingEnabled), basketAddr,
		)
	}
	if err != nil {
		tx.Rollback()
		log.Printf("indexer: seedOneBasket update meta(%s): %v", basketAddr, err)
		return
	}

	for i, row := range rows {
		_, err := tx.Exec(`
			INSERT INTO basket_constituents
				(basket_address, stock_address, symbol, target_weight_bps, display_order)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(basket_address, stock_address) DO NOTHING`,
			basketAddr, row.addr, row.symbol, row.weight, i,
		)
		if err != nil {
			tx.Rollback()
			log.Printf("indexer: seedOneBasket insert constituent(%s, %s): %v", basketAddr, row.addr, err)
			return
		}
	}

	if err := tx.Commit(); err != nil {
		log.Printf("indexer: seedOneBasket commit(%s): %v", basketAddr, err)
	} else {
		log.Printf("indexer: seeded %d constituents for basket %s", len(rows), basketAddr)
	}
}

// scanTransactionalEvents performs a cursor-based block scan for deposits,
// redemptions, rebalances, and fee snapshots. The cursor only advances after
// all logs in a chunk are confirmed written — a partially processed chunk
// leaves the cursor unchanged so the next startup re-scans it cleanly.
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
		_, _ = idx.db.Exec(
			`INSERT INTO sync_cursors (key, block_num) VALUES ('events', ?)`, fromBlock,
		)
	}

	latestHeader, err := client.HeaderByNumber(idx.ctx, nil)
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

	idx.mu.RLock()
	addresses := make([]common.Address, 0, len(idx.basketAddrs)+1)
	addresses = append(addresses, idx.registryAddr)
	for addr := range idx.basketAddrs {
		addresses = append(addresses, addr)
	}
	idx.mu.RUnlock()

	const chunkSize = int64(2000)

	for start := fromBlock; start <= toBlock; start += chunkSize {
		end := start + chunkSize - 1
		if end > toBlock {
			end = toBlock
		}

		query := ethereum.FilterQuery{
			FromBlock: big.NewInt(start),
			ToBlock:   big.NewInt(end),
			Addresses: addresses,
			Topics: [][]common.Hash{{
				topicDeposited,
				topicRedeemed,
				topicRebalanced,
				topicFeeSnapshoted,
			}},
		}

		logs, err := client.FilterLogs(idx.ctx, query)
		if err != nil {
			log.Printf("indexer: FilterLogs [%d-%d]: %v — stopping scan", start, end, err)
			return
		}

		// Process all logs in this chunk. Collect write errors. If any write
		// fails, stop scanning without advancing the cursor — the next startup
		// will re-scan this chunk from the last committed cursor position.
		chunkErr := false
		for _, vLog := range logs {
			if err := idx.handleLogErr(vLog); err != nil {
				log.Printf("indexer: chunk [%d-%d] log write error: %v — stopping scan", start, end, err)
				chunkErr = true
				break
			}
		}

		if chunkErr {
			return
		}

		// All logs in this chunk written successfully — advance cursor.
		_, _ = idx.db.Exec(
			`UPDATE sync_cursors SET block_num = ? WHERE key = 'events'`, end,
		)
	}

	log.Printf("indexer: transactional event scan complete through block %d", toBlock)
}

// handleLogErr routes a log to the appropriate handler and returns any write error.
// Used by scanTransactionalEvents where cursor safety requires knowing if writes succeeded.
func (idx *Indexer) handleLogErr(vLog types.Log) error {
	if len(vLog.Topics) == 0 {
		return nil
	}
	switch vLog.Topics[0] {
	case topicDeposited:
		return idx.handleDeposited(vLog)
	case topicRedeemed:
		return idx.handleRedeemed(vLog)
	case topicRebalanced:
		return idx.handleRebalanced(vLog)
	case topicFeeSnapshoted:
		return idx.handleFeeSnapshot(vLog)
	}
	return nil
}

// handleLog is used by the live subscription where we process all event types.
// Write errors are logged but do not stop the subscription loop.
func (idx *Indexer) handleLog(vLog types.Log) {
	if len(vLog.Topics) == 0 {
		return
	}
	switch vLog.Topics[0] {
	case topicBasketCreated:
		idx.handleBasketCreated(vLog)
	case topicDeposited:
		if err := idx.handleDeposited(vLog); err != nil {
			log.Printf("indexer: live handleDeposited: %v", err)
		}
	case topicRedeemed:
		if err := idx.handleRedeemed(vLog); err != nil {
			log.Printf("indexer: live handleRedeemed: %v", err)
		}
	case topicRebalanced:
		if err := idx.handleRebalanced(vLog); err != nil {
			log.Printf("indexer: live handleRebalanced: %v", err)
		}
	case topicFeeSnapshoted:
		if err := idx.handleFeeSnapshot(vLog); err != nil {
			log.Printf("indexer: live handleFeeSnapshot: %v", err)
		}
	case topicAssetAdded:
		idx.handleAssetAdded(vLog)
	case topicAssetDeact:
		idx.handleAssetDeactivated(vLog)
	case topicBasketSuspend:
		idx.handleBasketSuspended(vLog)
	}
}

// handleBasketCreated decodes all fields from the enriched event log.
// Constituent symbol lookups happen before the write transaction opens.
func (idx *Indexer) handleBasketCreated(vLog types.Log) {
	if len(vLog.Topics) < 4 {
		return
	}

	basket       := strings.ToLower(common.HexToAddress(vLog.Topics[1].Hex()).Hex())
	creatorToken := strings.ToLower(common.HexToAddress(vLog.Topics[2].Hex()).Hex())
	creator      := strings.ToLower(common.HexToAddress(vLog.Topics[3].Hex()).Hex())

	decoded, err := basketCreatedABI.Unpack(vLog.Data)
	if err != nil || len(decoded) < 6 {
		log.Printf("indexer: handleBasketCreated decode error %s: %v", basket, err)
		return
	}

	name,        _ := decoded[0].(string)
	symbol,      _ := decoded[1].(string)
	thesis,      _ := decoded[2].(string)
	constituents,_ := decoded[3].([]common.Address)
	targetWeights,_ := decoded[4].([]*big.Int)
	rebalancing, _ := decoded[5].(bool)

	// Look up all constituent symbols before opening the write transaction.
	type constituentRow struct {
		addr   string
		symbol string
		weight int64
	}
	cRows := make([]constituentRow, 0, len(constituents))
	for i, c := range constituents {
		cAddr := strings.ToLower(c.Hex())
		var cSymbol string
		idx.db.QueryRow(
			`SELECT symbol FROM supported_assets WHERE address = ?`, cAddr,
		).Scan(&cSymbol)
		weight := int64(0)
		if i < len(targetWeights) && targetWeights[i] != nil {
			weight = targetWeights[i].Int64()
		}
		cRows = append(cRows, constituentRow{addr: cAddr, symbol: cSymbol, weight: weight})
	}

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
		boolToInt(rebalancing), int64(vLog.BlockNumber), vLog.TxHash.Hex(),
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
			log.Printf("indexer: handleBasketCreated insert constituent: %v", err)
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
	name,   _ := decoded[1].(string)
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

func (idx *Indexer) handleDeposited(vLog types.Log) error {
	if len(vLog.Topics) < 2 || len(vLog.Data) < 96 {
		return nil
	}

	investor := common.HexToAddress(vLog.Topics[1].Hex())
	basket   := strings.ToLower(vLog.Address.Hex())

	usdgAmount   := new(big.Int).SetBytes(vLog.Data[0:32])
	tokensMinted := new(big.Int).SetBytes(vLog.Data[32:64])
	feeUsdg      := new(big.Int).SetBytes(vLog.Data[64:96])

	_, err := idx.db.Exec(`
		INSERT INTO deposits
			(basket_address, investor_address, usdg_amount, basket_tokens_minted, fee_usdg, timestamp, tx_hash)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		basket, strings.ToLower(investor.Hex()),
		usdgAmount.String(), tokensMinted.String(), feeUsdg.String(),
		int64(vLog.BlockNumber), vLog.TxHash.Hex(),
	)
	return err
}

func (idx *Indexer) handleRedeemed(vLog types.Log) error {
	if len(vLog.Topics) < 2 || len(vLog.Data) < 96 {
		return nil
	}

	investor := common.HexToAddress(vLog.Topics[1].Hex())
	basket   := strings.ToLower(vLog.Address.Hex())

	tokensBurned := new(big.Int).SetBytes(vLog.Data[0:32])
	usdgReturned := new(big.Int).SetBytes(vLog.Data[32:64])
	feeUsdg      := new(big.Int).SetBytes(vLog.Data[64:96])

	_, err := idx.db.Exec(`
		INSERT INTO redemptions
			(basket_address, investor_address, basket_tokens_burned, usdg_returned, fee_usdg, timestamp, tx_hash)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		basket, strings.ToLower(investor.Hex()),
		tokensBurned.String(), usdgReturned.String(), feeUsdg.String(),
		int64(vLog.BlockNumber), vLog.TxHash.Hex(),
	)
	return err
}

func (idx *Indexer) handleRebalanced(vLog types.Log) error {
	if len(vLog.Topics) < 2 {
		return nil
	}

	triggeredBy := common.HexToAddress(vLog.Topics[1].Hex())
	basket      := strings.ToLower(vLog.Address.Hex())

	_, err := idx.db.Exec(`
		INSERT INTO rebalances (basket_address, triggered_by, timestamp, tx_hash)
		VALUES (?, ?, ?, ?)`,
		basket, strings.ToLower(triggeredBy.Hex()),
		int64(vLog.BlockNumber), vLog.TxHash.Hex(),
	)
	return err
}

func (idx *Indexer) handleFeeSnapshot(vLog types.Log) error {
	if len(vLog.Topics) < 2 || len(vLog.Data) < 32 {
		return nil
	}

	basket     := strings.ToLower(vLog.Address.Hex())
	snapshotID := new(big.Int).SetBytes(vLog.Topics[1].Bytes()).Int64()
	usdgAmount := new(big.Int).SetBytes(vLog.Data[0:32])

	_, err := idx.db.Exec(`
		INSERT INTO fee_snapshots (basket_address, snapshot_id, usdg_amount, timestamp, tx_hash)
		VALUES (?, ?, ?, ?, ?)`,
		basket, snapshotID, usdgAmount.String(),
		int64(vLog.BlockNumber), vLog.TxHash.Hex(),
	)
	return err
}

func (idx *Indexer) handleAssetDeactivated(vLog types.Log) {
	if len(vLog.Topics) < 2 {
		return
	}
	token := strings.ToLower(common.HexToAddress(vLog.Topics[1].Hex()).Hex())
	_, err := idx.db.Exec(`UPDATE supported_assets SET is_active = 0 WHERE address = ?`, token)
	if err != nil {
		log.Printf("indexer: deactivate asset: %v", err)
	}
}

func (idx *Indexer) handleBasketSuspended(vLog types.Log) {
	basket := strings.ToLower(vLog.Address.Hex())
	_, err := idx.db.Exec(`UPDATE baskets SET suspended = 1 WHERE address = ?`, basket)
	if err != nil {
		log.Printf("indexer: suspend basket: %v", err)
	}
}

func (idx *Indexer) subscribe() error {
	client, err := ethclient.DialContext(idx.ctx, idx.wsURL)
	if err != nil {
		return err
	}
	defer client.Close()

	idx.mu.RLock()
	addresses := make([]common.Address, 0, len(idx.basketAddrs)+1)
	addresses = append(addresses, idx.registryAddr)
	for addr := range idx.basketAddrs {
		addresses = append(addresses, addr)
	}
	idx.mu.RUnlock()

	query := ethereum.FilterQuery{Addresses: addresses}

	logs := make(chan types.Log)
	sub, err := client.SubscribeFilterLogs(idx.ctx, query, logs)
	if err != nil {
		return err
	}
	defer sub.Unsubscribe()

	log.Println("indexer: live subscription active")

	for {
		select {
		case <-idx.ctx.Done():
			return nil
		case err := <-sub.Err():
			return err
		case vLog := <-logs:
			idx.handleLog(vLog)
		}
	}
}

func (idx *Indexer) newHTTPClient() (*ethclient.Client, error) {
	rpcURL := os.Getenv("RPC_URL")
	if rpcURL == "" {
		rpcURL = "https://rpc.testnet.chain.robinhood.com"
	}
	return ethclient.DialContext(idx.ctx, rpcURL)
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

func mustABIType(t string) abi.Type {
	typ, err := abi.NewType(t, "", nil)
	if err != nil {
		panic(err)
	}
	return typ
}
