package indexer

import (
	"context"
	"log"
	"math/big"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/OCD-Labs/Weave/server/db"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
)

// Event signatures — keccak256 of the event topic string.
// These must match the events emitted by the Solidity contracts exactly.
var (
	topicBasketCreated = eventTopic("BasketCreated(address,address,address,string,bool)")
	topicDeposited     = eventTopic("Deposited(address,uint256,uint256,uint256)")
	topicRedeemed      = eventTopic("Redeemed(address,uint256,uint256,uint256)")
	topicRebalanced    = eventTopic("Rebalanced(address)")
	topicFeeSnapshoted = eventTopic("RevenueSnapshoted(uint256,uint256,uint256)")
	topicAssetAdded    = eventTopic("AssetAdded(address,string,string)")
	topicAssetDeact    = eventTopic("AssetDeactivated(address)")
	topicBasketSuspend = eventTopic("Suspended()")
)

// Indexer subscribes to on-chain events and writes them to SQLite.
type Indexer struct {
	ctx          context.Context
	wsURL        string
	rpcURL       string
	registryAddr common.Address
	db           *db.DB

	// basketAddrs tracks all known basket addresses so we can filter their events.
	basketAddrs map[common.Address]bool
}

func New(ctx context.Context, wsURL, rpcURL, registryAddr string, database *db.DB) (*Indexer, error) {
	return &Indexer{
		ctx:          ctx,
		wsURL:        wsURL,
		rpcURL:       rpcURL,
		registryAddr: common.HexToAddress(registryAddr),
		db:           database,
		basketAddrs:  make(map[common.Address]bool),
	}, nil
}

// Run starts the event subscription loop. Reconnects on disconnect.
func (idx *Indexer) Run() {
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

func (idx *Indexer) subscribe() error {
	client, err := ethclient.DialContext(idx.ctx, idx.wsURL)
	if err != nil {
		return err
	}
	defer client.Close()

	// Load existing basket addresses from the database so we filter their events
	// correctly after a restart without re-indexing from genesis.
	idx.loadBasketAddrs()
	go idx.backfillAssets(client)
	go idx.backfillBaskets(client)

	// Build a broad filter: registry address + all known basket addresses.
	addresses := []common.Address{idx.registryAddr}
	for addr := range idx.basketAddrs {
		addresses = append(addresses, addr)
	}

	query := ethereum.FilterQuery{
		Addresses: addresses,
	}

	logs := make(chan types.Log)
	sub, err := client.SubscribeFilterLogs(idx.ctx, query, logs)
	if err != nil {
		return err
	}
	defer sub.Unsubscribe()

	log.Println("indexer: subscribed to on-chain events")

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

func (idx *Indexer) handleLog(vLog types.Log) {
	if len(vLog.Topics) == 0 {
		return
	}

	switch vLog.Topics[0] {
	case topicBasketCreated:
		idx.handleBasketCreated(vLog)
	case topicDeposited:
		idx.handleDeposited(vLog)
	case topicRedeemed:
		idx.handleRedeemed(vLog)
	case topicRebalanced:
		idx.handleRebalanced(vLog)
	case topicFeeSnapshoted:
		idx.handleFeeSnapshot(vLog)
	case topicAssetAdded:
		idx.handleAssetAdded(vLog)
	case topicAssetDeact:
		idx.handleAssetDeactivated(vLog)
	case topicBasketSuspend:
		idx.handleBasketSuspended(vLog)
	}
}

func (idx *Indexer) handleBasketCreated(vLog types.Log) {
	// Topics: [sig, basket indexed, creatorToken indexed, creator indexed]
	if len(vLog.Topics) < 4 {
		return
	}

	basket := common.HexToAddress(vLog.Topics[1].Hex())
	creatorToken := common.HexToAddress(vLog.Topics[2].Hex())
	creator := common.HexToAddress(vLog.Topics[3].Hex())
	txHash := vLog.TxHash.Hex()
	timestamp := int64(vLog.BlockNumber) // approximate; refined by price poller

	// Decode non-indexed fields: (string name, bool rebalancingEnabled)
	// We store empty name for now — the backend API reads name from contract state.
	rebalancing := false
	if len(vLog.Data) >= 64 {
		// bool is last word in ABI encoding
		rebalancing = vLog.Data[63] == 1
	}

	_, err := idx.db.Exec(`
		INSERT OR IGNORE INTO baskets
		(address, creator_token_address, creator_address, name, symbol, thesis,
		 rebalancing_enabled, created_at, created_tx, suspended)
		VALUES (?, ?, ?, '', '', '', ?, ?, ?, 0)`,
		strings.ToLower(basket.Hex()),
		strings.ToLower(creatorToken.Hex()),
		strings.ToLower(creator.Hex()),
		boolToInt(rebalancing),
		timestamp,
		txHash,
	)
	if err != nil {
		log.Printf("indexer: insert basket %s: %v", basket.Hex(), err)
		return
	}

	// Track this basket address so we subscribe to its events.
	idx.basketAddrs[basket] = true
	log.Printf("indexer: new basket %s", basket.Hex())
}

func (idx *Indexer) handleDeposited(vLog types.Log) {
	if len(vLog.Topics) < 2 {
		return
	}

	investor := common.HexToAddress(vLog.Topics[1].Hex())
	basket := strings.ToLower(vLog.Address.Hex())

	// Data: (uint256 usdgAmount, uint256 basketTokensMinted, uint256 feeUsdg)
	if len(vLog.Data) < 96 {
		return
	}

	usdgAmount := new(big.Int).SetBytes(vLog.Data[0:32])
	tokensMinted := new(big.Int).SetBytes(vLog.Data[32:64])
	feeUsdg := new(big.Int).SetBytes(vLog.Data[64:96])

	_, err := idx.db.Exec(`
		INSERT INTO deposits
		(basket_address, investor_address, usdg_amount, basket_tokens_minted, fee_usdg, timestamp, tx_hash)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		basket,
		strings.ToLower(investor.Hex()),
		usdgAmount.String(),
		tokensMinted.String(),
		feeUsdg.String(),
		int64(vLog.BlockNumber),
		vLog.TxHash.Hex(),
	)
	if err != nil {
		log.Printf("indexer: insert deposit: %v", err)
	}
}

func (idx *Indexer) handleRedeemed(vLog types.Log) {
	if len(vLog.Topics) < 2 {
		return
	}

	investor := common.HexToAddress(vLog.Topics[1].Hex())
	basket := strings.ToLower(vLog.Address.Hex())

	if len(vLog.Data) < 96 {
		return
	}

	tokensBurned := new(big.Int).SetBytes(vLog.Data[0:32])
	usdgReturned := new(big.Int).SetBytes(vLog.Data[32:64])
	feeUsdg := new(big.Int).SetBytes(vLog.Data[64:96])

	_, err := idx.db.Exec(`
		INSERT INTO redemptions
		(basket_address, investor_address, basket_tokens_burned, usdg_returned, fee_usdg, timestamp, tx_hash)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		basket,
		strings.ToLower(investor.Hex()),
		tokensBurned.String(),
		usdgReturned.String(),
		feeUsdg.String(),
		int64(vLog.BlockNumber),
		vLog.TxHash.Hex(),
	)
	if err != nil {
		log.Printf("indexer: insert redemption: %v", err)
	}
}

func (idx *Indexer) handleRebalanced(vLog types.Log) {
	if len(vLog.Topics) < 2 {
		return
	}

	triggeredBy := common.HexToAddress(vLog.Topics[1].Hex())
	basket := strings.ToLower(vLog.Address.Hex())

	_, err := idx.db.Exec(`
		INSERT INTO rebalances (basket_address, triggered_by, timestamp, tx_hash)
		VALUES (?, ?, ?, ?)`,
		basket,
		strings.ToLower(triggeredBy.Hex()),
		int64(vLog.BlockNumber),
		vLog.TxHash.Hex(),
	)
	if err != nil {
		log.Printf("indexer: insert rebalance: %v", err)
	}
}

func (idx *Indexer) handleFeeSnapshot(vLog types.Log) {
	if len(vLog.Topics) < 2 {
		return
	}

	basket := strings.ToLower(vLog.Address.Hex())
	snapshotID := new(big.Int).SetBytes(vLog.Topics[1].Bytes()).Int64()

	if len(vLog.Data) < 64 {
		return
	}

	usdgAmount := new(big.Int).SetBytes(vLog.Data[0:32])

	_, err := idx.db.Exec(`
		INSERT INTO fee_snapshots (basket_address, snapshot_id, usdg_amount, timestamp, tx_hash)
		VALUES (?, ?, ?, ?, ?)`,
		basket,
		snapshotID,
		usdgAmount.String(),
		int64(vLog.BlockNumber),
		vLog.TxHash.Hex(),
	)
	if err != nil {
		log.Printf("indexer: insert fee snapshot: %v", err)
	}
}

func (idx *Indexer) handleAssetAdded(vLog types.Log) {
	if len(vLog.Topics) < 2 {
		return
	}

	token := strings.ToLower(common.HexToAddress(vLog.Topics[1].Hex()).Hex())

	// Decode symbol and sector directly from log data.
	decoded, err := abi.Arguments{
		{Type: mustABIType("string")},
		{Type: mustABIType("string")},
	}.Unpack(vLog.Data)
	if err != nil {
		log.Printf("indexer: handleAssetAdded decode error: %v", err)
		idx.db.Exec(`
			INSERT OR IGNORE INTO supported_assets
			(address, symbol, name, sector, oracle_address, is_active, added_at)
			VALUES (?, '', '', '', '', 1, ?)`,
			token, int64(vLog.BlockNumber),
		)
		return
	}

	symbol := decoded[0].(string)
	sector := decoded[1].(string)

	// Get oracle address and name from registry.
	oracleAddr, name := idx.getAssetMeta(token)

	_, err = idx.db.Exec(`
			INSERT INTO supported_assets
			(address, symbol, name, sector, oracle_address, is_active, added_at)
			VALUES (?, ?, ?, ?, ?, 1, ?)
			ON CONFLICT(address) DO UPDATE SET
				symbol         = excluded.symbol,
				name           = excluded.name,
				sector         = excluded.sector,
				oracle_address = excluded.oracle_address,
				is_active      = excluded.is_active`,
		token,
		symbol,
		name,
		sector,
		oracleAddr,
		int64(vLog.BlockNumber),
	)
	if err != nil {
		log.Printf("indexer: handleAssetAdded insert error: %v", err)
	} else {
		log.Printf("indexer: asset indexed — %s (%s) oracle=%s", symbol, token, oracleAddr)
	}
}

func (idx *Indexer) getAssetMeta(tokenAddr string) (oracle string, name string) {
	httpClient, err := idx.newHTTPClient()
	if err != nil {
		return "", ""
	}
	defer httpClient.Close()

	assetsABI, _ := abi.JSON(strings.NewReader(`[{
		"inputs": [],
		"name": "getSupportedAssets",
		"outputs": [{
			"components": [
				{"internalType":"address","name":"tokenAddress",  "type":"address"},
				{"internalType":"address","name":"chainlinkFeed", "type":"address"},
				{"internalType":"string", "name":"symbol",        "type":"string"},
				{"internalType":"string", "name":"name",          "type":"string"},
				{"internalType":"string", "name":"sector",        "type":"string"},
				{"internalType":"bool",   "name":"active",        "type":"bool"}
			],
			"internalType":"struct IWeaveRegistry.AssetConfig[]",
			"name":"",
			"type":"tuple[]"
		}],
		"stateMutability":"view",
		"type":"function"
	}]`))

	data, err := httpClient.CallContract(idx.ctx, ethereum.CallMsg{
		To:   &idx.registryAddr,
		Data: assetsABI.Methods["getSupportedAssets"].ID,
	}, nil)
	if err != nil {
		log.Printf("indexer: getAssetMeta call error: %v", err)
		return "", ""
	}

	unpacked, err := assetsABI.Methods["getSupportedAssets"].Outputs.Unpack(data)
	if err != nil {
		log.Printf("indexer: getAssetMeta unpack error: %v", err)
		return "", ""
	}

	if len(unpacked) == 0 {
		return "", ""
	}

	items, ok := unpacked[0].([]struct {
		TokenAddress  common.Address `abi:"tokenAddress"`
		ChainlinkFeed common.Address `abi:"chainlinkFeed"`
		Symbol        string         `abi:"symbol"`
		Name          string         `abi:"name"`
		Sector        string         `abi:"sector"`
		Active        bool           `abi:"active"`
	})
	if !ok {
		rv := reflect.ValueOf(unpacked[0])
		if rv.Kind() != reflect.Slice {
			return "", ""
		}
		for i := 0; i < rv.Len(); i++ {
			elem := rv.Index(i)
			if elem.Kind() == reflect.Ptr {
				elem = elem.Elem()
			}
			tokenField  := elem.FieldByName("TokenAddress")
			oracleField := elem.FieldByName("ChainlinkFeed")
			nameField   := elem.FieldByName("Name")
			if !tokenField.IsValid() || !oracleField.IsValid() || !nameField.IsValid() {
				continue
			}
			addr, ok := tokenField.Interface().(common.Address)
			if !ok || strings.ToLower(addr.Hex()) != tokenAddr {
				continue
			}
			oracleAddrVal, ok := oracleField.Interface().(common.Address)
			if !ok {
				return "", ""
			}
			return strings.ToLower(oracleAddrVal.Hex()), nameField.String()
		}
		return "", ""
	}

	for _, a := range items {
		if strings.ToLower(a.TokenAddress.Hex()) == tokenAddr {
			return strings.ToLower(a.ChainlinkFeed.Hex()), a.Name
		}
	}

	return "", ""
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

func (idx *Indexer) loadBasketAddrs() {
	rows, err := idx.db.Query(`SELECT address FROM baskets`)
	if err != nil {
		return
	}
	defer rows.Close()

	for rows.Next() {
		var addr string
		if err := rows.Scan(&addr); err == nil {
			idx.basketAddrs[common.HexToAddress(addr)] = true
		}
	}
}

func (idx *Indexer) newHTTPClient() (*ethclient.Client, error) {
	// Use the HTTP RPC for log queries — WebSocket client can hang on eth_getLogs.
	rpcURL := os.Getenv("RPC_URL")
	if rpcURL == "" {
		rpcURL = "https://rpc.testnet.chain.robinhood.com"
	}
	return ethclient.DialContext(idx.ctx, rpcURL)
}

func (idx *Indexer) backfillAssets(_ *ethclient.Client) {
	httpClient, err := idx.newHTTPClient()
	if err != nil {
		log.Printf("indexer: backfill http client error: %v", err)
		return
	}
	defer httpClient.Close()

	deployBlock := big.NewInt(65989689)
	query := ethereum.FilterQuery{
		FromBlock: deployBlock,
		Addresses: []common.Address{idx.registryAddr},
		Topics:    [][]common.Hash{{topicAssetAdded}},
	}

	logs, err := httpClient.FilterLogs(idx.ctx, query)
	if err != nil {
		log.Printf("indexer: backfill assets error: %v", err)
		return
	}

	log.Printf("indexer: backfill found %d raw logs", len(logs))

	for _, vLog := range logs {
		idx.handleAssetAdded(vLog)
	}

	log.Printf("indexer: backfilled %d asset events", len(logs))
}

func (idx *Indexer) backfillBaskets(_ *ethclient.Client) {
	httpClient, err := idx.newHTTPClient()
	if err != nil {
		log.Printf("indexer: backfill http client error: %v", err)
		return
	}
	defer httpClient.Close()

	deployBlock := big.NewInt(65989689)
	query := ethereum.FilterQuery{
		FromBlock: deployBlock,
		Addresses: []common.Address{idx.registryAddr},
		Topics:    [][]common.Hash{{topicBasketCreated}},
	}

	logs, err := httpClient.FilterLogs(idx.ctx, query)
	if err != nil {
		log.Printf("indexer: backfill baskets error: %v", err)
		return
	}

	for _, vLog := range logs {
		idx.handleBasketCreated(vLog)
	}

	log.Printf("indexer: backfilled %d basket events", len(logs))
}

// eventTopic computes the keccak256 topic hash for an event signature string.
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
