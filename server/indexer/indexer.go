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

const rpcTimeout = 15 * time.Second
const chunkSize = int64(2000)
const seedMaxAttempts = 5
const blockTsCacheMax = 4096
const liveChunkSize = int64(50)
const livePollInterval = 2 * time.Second

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

// knownTopics is the complete set of event signatures this indexer handles.
// Passed as the sole filter when querying logs from the node. Address
// verification is performed in the handler against in-memory sets.
var knownTopics = [][]common.Hash{{
	topicBasketCreated,
	topicDeposited,
	topicRedeemed,
	topicRebalanced,
	topicFeeSnapshoted,
	topicAssetAdded,
	topicAssetDeact,
	topicBasketSuspend,
}}

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

	depositedABI = abi.Arguments{
		{Name: "usdgAmount",         Type: uint256Type},
		{Name: "basketTokensMinted", Type: uint256Type},
		{Name: "feeUsdg",            Type: uint256Type},
	}

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

type blockTsEntry struct {
	blockNumber uint64
	timestamp   uint64
}

// chunkAddrs holds basket and creator token addresses first seen within a
// single writeChunkAtomic call. BasketFactory emits Deposited before
// BasketCreated in the same transaction. A pre-pass over the chunk decodes
// all BasketCreated logs and populates this set before the main write loop
// runs, so Deposited events that precede their own BasketCreated in log order
// are still correctly attributed.
type chunkAddrs struct {
	baskets       map[common.Address]bool
	creatorTokens map[common.Address]common.Address
}

func newChunkAddrs() *chunkAddrs {
	return &chunkAddrs{
		baskets:       make(map[common.Address]bool),
		creatorTokens: make(map[common.Address]common.Address),
	}
}

// Indexer polls for new blocks on a fixed interval and fetches event logs as
// a batched range query using a topic-only filter. Address verification is
// performed against in-memory sets populated from the DB at startup and kept
// current as new baskets are indexed.
type Indexer struct {
	ctx          context.Context
	wsURL        string
	rpcURL       string
	factoryAddr  common.Address
	registryAddr common.Address
	deployBlock  int64
	db           *db.DB

	// basketAddrs and creatorTokenToBasket grow with basket deployments.
	// They are never evicted because every address represents a live contract
	// whose events must not be dropped.
	mu                   sync.RWMutex
	basketAddrs          map[common.Address]bool
	creatorTokenToBasket map[common.Address]common.Address

	blockTsMu   sync.Mutex
	blockTs     map[uint64]uint64
	blockTsFIFO []blockTsEntry
}

// New constructs an Indexer. Fails immediately if BASKET_FACTORY_ADDRESS is unset.
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
		ctx:                  ctx,
		wsURL:                wsURL,
		rpcURL:               rpcURL,
		registryAddr:         common.HexToAddress(registryAddr),
		factoryAddr:          common.HexToAddress(factoryAddrStr),
		deployBlock:          deployBlock,
		db:                   database,
		basketAddrs:          make(map[common.Address]bool),
		creatorTokenToBasket: make(map[common.Address]common.Address),
		blockTs:              make(map[uint64]uint64, blockTsCacheMax),
		blockTsFIFO:          make([]blockTsEntry, 0, blockTsCacheMax),
	}, nil
}

// Run starts the indexer. Chain state is synced first so the address sets are
// fully populated before any event processing begins. The historical scan and
// live poller then run sequentially on the same cursor.
func (idx *Indexer) Run() {
	idx.syncFromChain()
	idx.scanHistoricalEvents()
	idx.pollLiveBlocks()
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

	idx.mu.RLock()
	log.Printf("indexer: address sets ready — %d baskets, %d creator tokens",
		len(idx.basketAddrs), len(idx.creatorTokenToBasket))
	idx.mu.RUnlock()
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

	type basketEntry struct {
		basketCommon       common.Address
		creatorTokenCommon common.Address
		basket             string
		creatorToken       string
		creator            string
		createdAt          int64
		suspended          int
	}

	entries := make([]basketEntry, 0, rv.Len())

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

		basketCommon       := basketField.Interface().(common.Address)
		creatorTokenCommon := creatorTokenField.Interface().(common.Address)
		suspended := 0
		if !activeField.Bool() {
			suspended = 1
		}

		entries = append(entries, basketEntry{
			basketCommon:       basketCommon,
			creatorTokenCommon: creatorTokenCommon,
			basket:             strings.ToLower(basketCommon.Hex()),
			creatorToken:       strings.ToLower(creatorTokenCommon.Hex()),
			creator:            strings.ToLower(creatorField.Interface().(common.Address).Hex()),
			createdAt:          createdAtField.Interface().(*big.Int).Int64(),
			suspended:          suspended,
		})
	}

	tx, err := idx.db.Begin()
	if err != nil {
		log.Printf("indexer: syncBaskets begin tx: %v", err)
		return
	}

	for _, e := range entries {
		_, err := tx.Exec(`
			INSERT INTO baskets
				(address, creator_token_address, creator_address, name, symbol, thesis,
				 rebalancing_enabled, created_at, created_tx, suspended)
			VALUES (?, ?, ?, '', '', '', 0, ?, '', ?)
			ON CONFLICT(address) DO UPDATE SET
				suspended = excluded.suspended`,
			e.basket, e.creatorToken, e.creator, e.createdAt, e.suspended,
		)
		if err != nil {
			tx.Rollback()
			log.Printf("indexer: syncBaskets upsert %s: %v — rolling back", e.basket, err)
			return
		}
	}

	if err := tx.Commit(); err != nil {
		log.Printf("indexer: syncBaskets commit: %v", err)
		return
	}

	idx.mu.Lock()
	for _, e := range entries {
		idx.basketAddrs[e.basketCommon] = true
		idx.creatorTokenToBasket[e.creatorTokenCommon] = e.basketCommon
	}
	idx.mu.Unlock()

	log.Printf("indexer: synced %d baskets from chain", len(entries))
}

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

	work := make(chan string)
	go func() {
		for _, addr := range needsSeeding {
			work <- addr
		}
		close(work)
	}()

	const workers = 5
	var wg sync.WaitGroup
	n := workers
	if len(needsSeeding) < n {
		n = len(needsSeeding)
	}
	for i := 0; i < n; i++ {
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

// scanHistoricalEvents replays all blocks from the stored cursor to chain tip
// using chunked eth_getLogs calls filtered by topic only.
func (idx *Indexer) scanHistoricalEvents() {
	client, err := idx.newHTTPClient()
	if err != nil {
		log.Printf("indexer: scanHistoricalEvents dial error: %v", err)
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
			log.Printf("indexer: scanHistoricalEvents: failed to initialise cursor: %v", err)
			return
		}
	}

	hctx, hcancel := context.WithTimeout(idx.ctx, rpcTimeout)
	latestHeader, err := client.HeaderByNumber(hctx, nil)
	hcancel()
	if err != nil {
		log.Printf("indexer: scanHistoricalEvents latest block error: %v", err)
		return
	}
	toBlock := latestHeader.Number.Int64()

	if fromBlock >= toBlock {
		log.Printf("indexer: event cursor current at block %d", fromBlock)
		return
	}

	log.Printf("indexer: scanning historical events blocks %d → %d", fromBlock, toBlock)

	for start := fromBlock; start <= toBlock; start += chunkSize {
		select {
		case <-idx.ctx.Done():
			log.Printf("indexer: historical scan interrupted at block %d", start)
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
			Topics:    knownTopics,
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

	log.Printf("indexer: historical scan complete through block %d", toBlock)
}

// pollLiveBlocks runs after the historical scan completes and polls for new
// blocks on a fixed interval. Each tick fetches at most liveChunkSize blocks
// as a single eth_getLogs call, advancing the cursor on every successful
// write.
func (idx *Indexer) pollLiveBlocks() {
	client, err := idx.newHTTPClient()
	if err != nil {
		log.Printf("indexer: pollLiveBlocks dial error: %v", err)
		return
	}
	defer client.Close()

	log.Printf("indexer: live polling active (interval=%s, chunkSize=%d)", livePollInterval, liveChunkSize)

	ticker := time.NewTicker(livePollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-idx.ctx.Done():
			return
		case <-ticker.C:
			if err := idx.pollOnce(client); err != nil {
				log.Printf("indexer: pollOnce error: %v", err)
			}
		}
	}
}

// pollOnce fetches the next batch of blocks from the cursor to the current
// chain tip, capped at liveChunkSize, and writes any matching logs atomically.
func (idx *Indexer) pollOnce(client *ethclient.Client) error {
	var fromBlock int64
	if err := idx.db.QueryRow(
		`SELECT block_num FROM sync_cursors WHERE key = 'events'`,
	).Scan(&fromBlock); err != nil {
		return fmt.Errorf("read cursor: %w", err)
	}

	hctx, hcancel := context.WithTimeout(idx.ctx, rpcTimeout)
	latestHeader, err := client.HeaderByNumber(hctx, nil)
	hcancel()
	if err != nil {
		return fmt.Errorf("latest block: %w", err)
	}
	toBlock := latestHeader.Number.Int64()

	if fromBlock >= toBlock {
		return nil
	}

	end := fromBlock + liveChunkSize
	if end > toBlock {
		end = toBlock
	}

	filterCtx, filterCancel := context.WithTimeout(idx.ctx, rpcTimeout)
	logs, err := client.FilterLogs(filterCtx, ethereum.FilterQuery{
		FromBlock: big.NewInt(fromBlock + 1),
		ToBlock:   big.NewInt(end),
		Topics:    knownTopics,
	})
	filterCancel()

	if err != nil {
		return fmt.Errorf("FilterLogs [%d-%d]: %w", fromBlock+1, end, err)
	}

	return idx.writeChunkAtomic(client, logs, end)
}

// writeChunkAtomic writes all logs for a block or scan chunk atomically and
// advances the event cursor to endBlock.
//
// BasketFactory emits Deposited before BasketCreated in the same transaction,
// so both appear in the same block and therefore the same chunk. A pre-pass
// processes all BasketCreated logs first and populates the chunk-local address
// set so that Deposited events from the same transaction are correctly
// attributed before the global sets are updated after commit.
func (idx *Indexer) writeChunkAtomic(client *ethclient.Client, logs []types.Log, endBlock int64) error {
	tx, err := idx.db.Begin()
	if err != nil {
		return fmt.Errorf("begin chunk tx: %w", err)
	}

	local := newChunkAddrs()

	// Pre-pass: decode all BasketCreated logs and write basket rows first.
	// This populates the local address set before the main loop processes
	// Deposited and RevenueSnapshoted events from the same transactions.
	for _, vLog := range logs {
		if len(vLog.Topics) == 0 || vLog.Topics[0] != topicBasketCreated {
			continue
		}
		if vLog.Address != idx.factoryAddr {
			continue
		}
		if err := idx.writeBasketCreated(tx, client, vLog, local); err != nil {
			tx.Rollback()
			return fmt.Errorf("writeBasketCreated tx=%s: %w", vLog.TxHash.Hex(), err)
		}
	}

	// Main pass: process all non-BasketCreated logs in emission order.
	for _, vLog := range logs {
		if len(vLog.Topics) == 0 || vLog.Topics[0] == topicBasketCreated {
			continue
		}
		if err := idx.writeLog(tx, client, vLog, local); err != nil {
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

	if err := tx.Commit(); err != nil {
		return err
	}

	// Merge chunk-local sets into global sets after commit.
	idx.mu.Lock()
	for addr := range local.baskets {
		idx.basketAddrs[addr] = true
	}
	for ct, basket := range local.creatorTokens {
		idx.creatorTokenToBasket[ct] = basket
	}
	idx.mu.Unlock()

	return nil
}

// writeLog routes a single non-BasketCreated log to its handler after
// verifying the emitting address belongs to this protocol.
func (idx *Indexer) writeLog(tx *sql.Tx, client *ethclient.Client, vLog types.Log, local *chunkAddrs) error {
	if len(vLog.Topics) == 0 {
		return nil
	}

	topic := vLog.Topics[0]

	if topic == topicAssetAdded || topic == topicAssetDeact {
		if vLog.Address != idx.registryAddr {
			return nil
		}
		if topic == topicAssetAdded {
			idx.handleAssetAdded(vLog)
		} else {
			idx.handleAssetDeactivated(vLog)
		}
		return nil
	}

	if topic == topicBasketSuspend {
		if !idx.isKnownBasket(vLog.Address, local) {
			return nil
		}
		idx.handleBasketSuspended(vLog)
		return nil
	}

	if topic == topicDeposited || topic == topicRedeemed || topic == topicRebalanced {
		if !idx.isKnownBasket(vLog.Address, local) {
			return nil
		}
		switch topic {
		case topicDeposited:
			return idx.writeDeposited(tx, client, vLog)
		case topicRedeemed:
			return idx.writeRedeemed(tx, client, vLog)
		case topicRebalanced:
			return idx.writeRebalanced(tx, client, vLog)
		}
	}

	if topic == topicFeeSnapshoted {
		if !idx.isKnownCreatorToken(vLog.Address, local) {
			return nil
		}
		return idx.writeFeeSnapshot(tx, client, vLog, local)
	}

	return nil
}

// isKnownBasket returns true if addr is a known basket proxy, checking the
// global set then the chunk-local set.
func (idx *Indexer) isKnownBasket(addr common.Address, local *chunkAddrs) bool {
	idx.mu.RLock()
	known := idx.basketAddrs[addr]
	idx.mu.RUnlock()
	if known {
		return true
	}
	return local.baskets[addr]
}

// isKnownCreatorToken returns true if addr is a known creator token contract,
// checking the global map then the chunk-local map.
func (idx *Indexer) isKnownCreatorToken(addr common.Address, local *chunkAddrs) bool {
	idx.mu.RLock()
	_, known := idx.creatorTokenToBasket[addr]
	idx.mu.RUnlock()
	if known {
		return true
	}
	_, localKnown := local.creatorTokens[addr]
	return localKnown
}

// writeBasketCreated writes the basket and constituent rows within the provided
// transaction and registers the new addresses in the chunk-local set immediately
// so subsequent logs in the same chunk that reference this basket are correctly
// handled before the global sets are updated after commit.
func (idx *Indexer) writeBasketCreated(tx *sql.Tx, client *ethclient.Client, vLog types.Log, local *chunkAddrs) error {
	if len(vLog.Topics) < 4 {
		log.Printf("indexer: writeBasketCreated: expected 4 topics, got %d — tx=%s", len(vLog.Topics), vLog.TxHash.Hex())
		return nil
	}

	basketCommon       := common.HexToAddress(vLog.Topics[1].Hex())
	creatorTokenCommon := common.HexToAddress(vLog.Topics[2].Hex())
	basket             := strings.ToLower(basketCommon.Hex())
	creatorToken       := strings.ToLower(creatorTokenCommon.Hex())
	creator            := strings.ToLower(common.HexToAddress(vLog.Topics[3].Hex()).Hex())

	decoded, err := basketCreatedABI.Unpack(vLog.Data)
	if err != nil || len(decoded) < 6 {
		log.Printf("indexer: writeBasketCreated decode error tx=%s: %v", vLog.TxHash.Hex(), err)
		return nil
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
			log.Printf("indexer: writeBasketCreated symbol lookup %s: %v", cAddr, err)
		}
		weight := int64(0)
		if i < len(targetWeights) && targetWeights[i] != nil {
			weight = targetWeights[i].Int64()
		}
		cRows = append(cRows, constituentRow{addr: cAddr, symbol: cSymbol, weight: weight})
	}

	createdAt := idx.blockTimestamp(client, vLog.BlockNumber)

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
		return fmt.Errorf("insert basket %s: %w", basket, err)
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
			return fmt.Errorf("insert constituent %s for basket %s: %w", row.addr, basket, err)
		}
	}

	local.baskets[basketCommon] = true
	local.creatorTokens[creatorTokenCommon] = basketCommon

	log.Printf("indexer: basket %s written with %d constituents (creatorToken=%s)", basket, len(cRows), creatorToken)
	return nil
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

// writeFeeSnapshot handles RevenueSnapshoted events emitted by CreatorToken
// contracts. vLog.Address is the CreatorToken contract address, not the basket
// proxy. The basket address is resolved from creatorTokenToBasket then the
// chunk-local map.
func (idx *Indexer) writeFeeSnapshot(tx *sql.Tx, client *ethclient.Client, vLog types.Log, local *chunkAddrs) error {
	if len(vLog.Topics) < 2 {
		return nil
	}

	creatorTokenCommon := vLog.Address

	idx.mu.RLock()
	basketCommon, found := idx.creatorTokenToBasket[creatorTokenCommon]
	idx.mu.RUnlock()

	if !found {
		basketCommon, found = local.creatorTokens[creatorTokenCommon]
	}

	if !found {
		log.Printf("indexer: writeFeeSnapshot: no basket for creatorToken %s — skipping",
			strings.ToLower(creatorTokenCommon.Hex()))
		return nil
	}

	basketAddr := strings.ToLower(basketCommon.Hex())
	snapshotID := new(big.Int).SetBytes(vLog.Topics[1].Bytes()).Int64()

	decoded, err := feeSnapshotABI.Unpack(vLog.Data)
	if err != nil || len(decoded) < 1 {
		return fmt.Errorf("RevenueSnapshoted unpack tx=%s: %w", vLog.TxHash.Hex(), err)
	}

	usdgAmount := decoded[0].(*big.Int)
	ts         := idx.blockTimestamp(client, vLog.BlockNumber)

	_, err = tx.Exec(`
		INSERT INTO fee_snapshots (basket_address, snapshot_id, usdg_amount, timestamp, tx_hash, log_index)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(tx_hash, log_index) DO NOTHING`,
		basketAddr, snapshotID, usdgAmount.String(), ts, vLog.TxHash.Hex(), vLog.Index,
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

// blockTimestamp returns the Unix timestamp for a block from a bounded FIFO
// cache, fetching via RPC on a miss.
func (idx *Indexer) blockTimestamp(client *ethclient.Client, blockNumber uint64) int64 {
	idx.blockTsMu.Lock()
	ts, ok := idx.blockTs[blockNumber]
	idx.blockTsMu.Unlock()
	if ok {
		return int64(ts)
	}

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

	fetched := header.Time

	idx.blockTsMu.Lock()
	defer idx.blockTsMu.Unlock()

	if len(idx.blockTsFIFO) >= blockTsCacheMax {
		oldest := idx.blockTsFIFO[0]
		idx.blockTsFIFO = idx.blockTsFIFO[1:]
		delete(idx.blockTs, oldest.blockNumber)
	}
	idx.blockTs[blockNumber] = fetched
	idx.blockTsFIFO = append(idx.blockTsFIFO, blockTsEntry{blockNumber: blockNumber, timestamp: fetched})

	return int64(fetched)
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

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

func getEnv(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}