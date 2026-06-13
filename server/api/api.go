package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/OCD-Labs/Weave/server/ai"
	"github.com/OCD-Labs/Weave/server/db"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
)

// rpcTimeout is the per-call deadline applied to every outbound RPC request
// made by the API handlers.
const rpcTimeout = 15 * time.Second

// basketStateABI is parsed once at startup and reused across all handler
// invocations.
var basketStateABI abi.ABI

// claimableRevenueABI is parsed once at startup and reused by getClaimableSnapshots.
var claimableRevenueABI abi.ABI

func init() {
	var err error

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
		panic(fmt.Sprintf("api: parse basketStateABI: %v", err))
	}

	claimableRevenueABI, err = abi.JSON(strings.NewReader(`[{
		"inputs":[
			{"internalType":"address","name":"account",    "type":"address"},
			{"internalType":"uint256","name":"snapshotId", "type":"uint256"}
		],
		"name":"claimableRevenue",
		"outputs":[{"internalType":"uint256","name":"","type":"uint256"}],
		"stateMutability":"view",
		"type":"function"
	}]`))
	if err != nil {
		panic(fmt.Sprintf("api: parse claimableRevenueABI: %v", err))
	}
}

// snapshotEntry is shared between getCreatorDashboard and getClaimableSnapshots.
type snapshotEntry struct {
	SnapshotID    int64  `json:"snapshotId"`
	UsdgAmount    string `json:"usdgAmount"`
	Timestamp     int64  `json:"timestamp"`
	TxHash        string `json:"txHash"`
	ClaimableUsdg string `json:"claimableByWallet"`
}

// NewRouter wires all HTTP routes. The RPC client is initialised once here
// and shared across all handlers that need it, avoiding per-request dial
// overhead on cache misses.
func NewRouter(database *db.DB, openAIKey string, openAIModel string) http.Handler {
	mux := http.NewServeMux()

	rpcURL := os.Getenv("RPC_URL")
	if rpcURL == "" {
		rpcURL = "https://rpc.testnet.chain.robinhood.com"
	}

	dialCtx, dialCancel := context.WithTimeout(context.Background(), rpcTimeout)
	defer dialCancel()

	rpcClient, err := ethclient.DialContext(dialCtx, rpcURL)
	if err != nil {
		log.Printf("api: RPC dial warning: %v — RPC-dependent handlers will degrade gracefully", err)
		rpcClient = nil
	}

	h := &handler{
		db:          database,
		openAIKey:   openAIKey,
		openAIModel: openAIModel,
		rpcClient:   rpcClient,
	}

	mux.HandleFunc("GET /baskets", h.listBaskets)
	mux.HandleFunc("GET /baskets/{address}", h.getBasket)
	mux.HandleFunc("GET /baskets/{address}/performance", h.getBasketPerformance)
	mux.HandleFunc("GET /baskets/{address}/positions/{wallet}", h.getPosition)
	mux.HandleFunc("GET /catalogue", h.getCatalogue)
	mux.HandleFunc("GET /catalogue/{address}", h.getCatalogueAsset)
	mux.HandleFunc("GET /prices", h.getPrices)
	mux.HandleFunc("GET /positions/{wallet}", h.getPortfolio)
	mux.HandleFunc("GET /creator/{wallet}", h.getCreatorDashboard)
	mux.HandleFunc("GET /creator-tokens/{address}", h.getCreatorToken)
	mux.HandleFunc("POST /ai/compose", h.aiCompose)
	mux.HandleFunc("GET /docs", h.serveDocs)
	mux.HandleFunc("GET /openapi.json", h.serveOpenAPI)

	return corsMiddleware(mux)
}

type handler struct {
	db          *db.DB
	openAIKey   string
	openAIModel string
	rpcClient   *ethclient.Client
}

// Marketplace

type BasketSummary struct {
	Address          string               `json:"address"`
	CreatorToken     string               `json:"creatorToken"`
	Creator          string               `json:"creator"`
	Name             string               `json:"name"`
	Symbol           string               `json:"symbol"`
	Thesis           string               `json:"thesis"`
	Rebalancing      bool                 `json:"rebalancingEnabled"`
	DriftThreshold   *int64               `json:"driftThresholdBps"`
	CreatedAt        int64                `json:"createdAt"`
	Suspended        bool                 `json:"suspended"`
	NavPerToken      string               `json:"navPerToken"`
	TotalValueUsdg   string               `json:"totalValueUsdg"`
	NavChange24hPct  string               `json:"navChange24hPct"`
	ConstituentCount int                  `json:"constituentCount"`
	Constituents     []constituentSummary `json:"constituents"`
}

func (h *handler) listBaskets(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.Query(`
		SELECT
			b.address,
			b.creator_token_address,
			b.creator_address,
			b.name,
			b.symbol,
			b.thesis,
			b.rebalancing_enabled,
			b.drift_threshold_bps,
			b.created_at,
			b.suspended,
			COALESCE(n.nav_per_token,    '0') AS nav_per_token,
			COALESCE(n.total_value_usdg, '0') AS total_value_usdg,
			COALESCE(n24.nav_per_token,  '0') AS nav_24h_ago
		FROM baskets b
		LEFT JOIN (
			SELECT basket_address, nav_per_token, total_value_usdg
			FROM nav_history
			WHERE (basket_address, timestamp) IN (
				SELECT basket_address, MAX(timestamp)
				FROM nav_history GROUP BY basket_address
			)
		) n ON n.basket_address = b.address
		LEFT JOIN (
			SELECT basket_address, nav_per_token
			FROM nav_history n1
			WHERE timestamp = (
				SELECT MAX(timestamp) FROM nav_history n2
				WHERE n2.basket_address = n1.basket_address
				AND n2.timestamp <= strftime('%s','now') - 86400
			)
		) n24 ON n24.basket_address = b.address
		ORDER BY b.created_at DESC`)
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var order []string
	byAddr := make(map[string]*BasketSummary)

	for rows.Next() {
		var b BasketSummary
		var rebal, susp int
		var drift sql.NullInt64
		var nav24hAgo string

		if err := rows.Scan(
			&b.Address, &b.CreatorToken, &b.Creator,
			&b.Name, &b.Symbol, &b.Thesis,
			&rebal, &drift, &b.CreatedAt, &susp,
			&b.NavPerToken, &b.TotalValueUsdg, &nav24hAgo,
		); err != nil {
			continue
		}

		b.Rebalancing = rebal == 1
		b.Suspended = susp == 1
		b.NavChange24hPct = pctChange(nav24hAgo, b.NavPerToken)
		b.Constituents = []constituentSummary{}
		if drift.Valid {
			b.DriftThreshold = &drift.Int64
		}

		order = append(order, b.Address)
		byAddr[b.Address] = &b
	}

	if len(byAddr) == 0 {
		jsonOK(w, []BasketSummary{})
		return
	}

	cRows, err := h.db.Query(`
		SELECT bc.basket_address, bc.stock_address, bc.symbol, bc.target_weight_bps, COALESCE(sa.sector,'')
		FROM basket_constituents bc
		LEFT JOIN supported_assets sa ON sa.address = bc.stock_address
		ORDER BY bc.basket_address, bc.display_order`)
	if err == nil {
		defer cRows.Close()
		for cRows.Next() {
			var basketAddr, stockAddr, sym, sector string
			var weight int64
			if cRows.Scan(&basketAddr, &stockAddr, &sym, &weight, &sector) == nil {
				if b, ok := byAddr[basketAddr]; ok {
					b.Constituents = append(b.Constituents, constituentSummary{
						Address:         stockAddr,
						Symbol:          sym,
						TargetWeightBps: weight,
						Sector:          sector,
					})
					b.ConstituentCount = len(b.Constituents)
				}
			}
		}
	}

	result := make([]BasketSummary, 0, len(order))
	for _, addr := range order {
		result = append(result, *byAddr[addr])
	}

	jsonOK(w, result)
}

type constituentSummary struct {
	Address         string `json:"address"`
	Symbol          string `json:"symbol"`
	TargetWeightBps int64  `json:"targetWeightBps"`
	Sector          string `json:"sector"`
}

// Basket Detail

func (h *handler) getBasket(w http.ResponseWriter, r *http.Request) {
	addr := strings.ToLower(r.PathValue("address"))

	var b struct {
		Address        string `json:"address"`
		CreatorToken   string `json:"creatorToken"`
		Creator        string `json:"creator"`
		Name           string `json:"name"`
		Symbol         string `json:"symbol"`
		Thesis         string `json:"thesis"`
		Rebalancing    bool   `json:"rebalancingEnabled"`
		DriftThreshold *int64 `json:"driftThresholdBps"`
		CreatedAt      int64  `json:"createdAt"`
		Suspended      bool   `json:"suspended"`
	}

	var rebal, susp int
	var drift sql.NullInt64

	err := h.db.QueryRow(`
		SELECT address, creator_token_address, creator_address, name, symbol, thesis,
		       rebalancing_enabled, drift_threshold_bps, created_at, suspended
		FROM baskets WHERE address = ?`, addr).Scan(
		&b.Address, &b.CreatorToken, &b.Creator,
		&b.Name, &b.Symbol, &b.Thesis,
		&rebal, &drift, &b.CreatedAt, &susp,
	)
	if err == sql.ErrNoRows {
		jsonError(w, "basket not found", http.StatusNotFound)
		return
	}
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}

	b.Rebalancing = rebal == 1
	b.Suspended = susp == 1
	if drift.Valid {
		b.DriftThreshold = &drift.Int64
	}

	// Live basket state — read from DB cache, refresh if stale (> 30s).
	state, err := h.getBasketStateFromCache(r.Context(), addr)
	if err != nil {
		log.Printf("api: getBasketStateFromCache(%s): %v", addr, err)
	}

	// Performance history, rebalance history, deposit history, and redemption history.
	perf := []PerfEntry{}
	rebalHistory := []RebalanceEntry{}
	depHistory := []DepositEntry{}
	redemptHistory := []RedemptionEntry{}

	readTx, err := h.db.Begin()
	if err != nil {
		log.Printf("api: getBasket begin read tx: %v", err)
	} else {
		// Performance history.
		if perfRows, err := readTx.Query(`
			SELECT nav_per_token, total_value_usdg, timestamp
			FROM nav_history WHERE basket_address = ?
			ORDER BY timestamp ASC`, addr); err == nil {
			defer perfRows.Close()
			for perfRows.Next() {
				var p PerfEntry
				if perfRows.Scan(&p.NavPerToken, &p.TotalValueUsdg, &p.Timestamp) == nil {
					perf = append(perf, p)
				}
			}
		}

		// Rebalance history.
		if rebRows, err := readTx.Query(`
			SELECT timestamp, tx_hash, triggered_by FROM rebalances
			WHERE basket_address = ? ORDER BY timestamp DESC LIMIT 50`, addr); err == nil {
			defer rebRows.Close()
			for rebRows.Next() {
				var e RebalanceEntry
				if rebRows.Scan(&e.Timestamp, &e.TxHash, &e.TriggeredBy) == nil {
					rebalHistory = append(rebalHistory, e)
				}
			}
		}

		// Deposit history.
		if depRows, err := readTx.Query(`
			SELECT investor_address, usdg_amount, basket_tokens_minted, timestamp, tx_hash
			FROM deposits WHERE basket_address = ? ORDER BY timestamp DESC LIMIT 50`, addr); err == nil {
			defer depRows.Close()
			for depRows.Next() {
				var e DepositEntry
				if depRows.Scan(&e.Investor, &e.UsdgAmount, &e.BasketTokensMinted, &e.Timestamp, &e.TxHash) == nil {
					depHistory = append(depHistory, e)
				}
			}
		}

		// Redemption history.
		if redRows, err := readTx.Query(`
			SELECT investor_address, usdg_returned, basket_tokens_burned, timestamp, tx_hash
			FROM redemptions WHERE basket_address = ? ORDER BY timestamp DESC LIMIT 50`, addr); err == nil {
			defer redRows.Close()
			for redRows.Next() {
				var e RedemptionEntry
				if redRows.Scan(&e.Investor, &e.UsdgReturned, &e.BasketTokensBurned, &e.Timestamp, &e.TxHash) == nil {
					redemptHistory = append(redemptHistory, e)
				}
			}
		}

		// Read-only transaction — rollback is a no-op but correct.
		readTx.Rollback()
	}

	navPerToken := "0"
	totalValueUsdg := "0"
	var maxDriftBps int64
	var needsRebalancing bool

	if state != nil {
		navPerToken = state.NavPerToken
		totalValueUsdg = state.TotalValueUsdg
		maxDriftBps = state.MaxDriftBps
		needsRebalancing = state.NeedsRebalancing
	} else {
		var dbNav, dbTv string
		if h.db.QueryRow(`
			SELECT nav_per_token, total_value_usdg FROM nav_history
			WHERE basket_address = ? ORDER BY timestamp DESC LIMIT 1`, addr,
		).Scan(&dbNav, &dbTv) == nil {
			navPerToken = dbNav
			totalValueUsdg = dbTv
		}
	}

	navChange24h := h.navChangePct(addr, 86400)
	navChange7d := h.navChangePct(addr, 86400*7)
	navChange30d := h.navChangePct(addr, 86400*30)

	// Use the raw JSON string directly — avoids an unmarshal+re-marshal.
	constituentsRaw := json.RawMessage("[]")
	if state != nil && state.ConstituentsJSON != "" {
		constituentsRaw = json.RawMessage(state.ConstituentsJSON)
	}

	jsonOK(w, BasketDetailResponse{
		Address:            b.Address,
		CreatorToken:       b.CreatorToken,
		Creator:            b.Creator,
		Name:               b.Name,
		Symbol:             b.Symbol,
		Thesis:             b.Thesis,
		RebalancingEnabled: b.Rebalancing,
		DriftThresholdBps:  b.DriftThreshold,
		CreatedAt:          b.CreatedAt,
		Suspended:          b.Suspended,
		NavPerToken:        navPerToken,
		TotalValueUsdg:     totalValueUsdg,
		NavChange24hPct:    navChange24h,
		NavChange7dPct:     navChange7d,
		NavChange30dPct:    navChange30d,
		MaxDriftBps:        maxDriftBps,
		NeedsRebalancing:   needsRebalancing,
		Constituents:       constituentsRaw,
		PerformanceHistory: perf,
		RebalanceHistory:   rebalHistory,
		DepositHistory:     depHistory,
		RedemptionHistory:  redemptHistory,
	})
}

type PerfEntry struct {
	NavPerToken    string `json:"navPerToken"`
	TotalValueUsdg string `json:"totalValueUsdg"`
	Timestamp      int64  `json:"timestamp"`
}
type RebalanceEntry struct {
	Timestamp   int64  `json:"timestamp"`
	TxHash      string `json:"txHash"`
	TriggeredBy string `json:"triggeredBy"`
}
type DepositEntry struct {
	Investor           string `json:"investor"`
	UsdgAmount         string `json:"usdgAmount"`
	BasketTokensMinted string `json:"basketTokensMinted"`
	Timestamp          int64  `json:"timestamp"`
	TxHash             string `json:"txHash"`
}
type RedemptionEntry struct {
	Investor           string `json:"investor"`
	UsdgReturned       string `json:"usdgReturned"`
	BasketTokensBurned string `json:"basketTokensBurned"`
	Timestamp          int64  `json:"timestamp"`
	TxHash             string `json:"txHash"`
}

type BasketDetailResponse struct {
	Address            string          `json:"address"`
	CreatorToken       string          `json:"creatorToken"`
	Creator            string          `json:"creator"`
	Name               string          `json:"name"`
	Symbol             string          `json:"symbol"`
	Thesis             string          `json:"thesis"`
	RebalancingEnabled bool            `json:"rebalancingEnabled"`
	DriftThresholdBps  *int64          `json:"driftThresholdBps"`
	CreatedAt          int64           `json:"createdAt"`
	Suspended          bool            `json:"suspended"`
	NavPerToken        string          `json:"navPerToken"`
	TotalValueUsdg     string          `json:"totalValueUsdg"`
	NavChange24hPct    string          `json:"navChange24hPct"`
	NavChange7dPct     string          `json:"navChange7dPct"`
	NavChange30dPct    string          `json:"navChange30dPct"`
	MaxDriftBps        int64           `json:"maxDriftBps"`
	NeedsRebalancing   bool            `json:"needsRebalancing"`
	Constituents       json.RawMessage `json:"constituents"`
	PerformanceHistory any             `json:"performanceHistory"`
	RebalanceHistory   any             `json:"rebalanceHistory"`
	DepositHistory     any             `json:"depositHistory"`
	RedemptionHistory  any             `json:"redemptionHistory"`
}

// basketStateCache is the shape stored in and read from basket_state_cache.
type basketStateCache struct {
	BasketAddress      string
	ConstituentsJSON   string
	CurrentWeightsJSON string
	BalancesJSON       string
	TotalValueUsdg     string
	NavPerToken        string
	MaxDriftBps        int64
	NeedsRebalancing   bool
	CachedAt           int64
}

// getBasketStateFromCache reads from basket_state_cache if the entry is
// within 30 seconds. If stale or absent, reads live from the RPC node,
// writes the result back to the cache, and returns it.
func (h *handler) getBasketStateFromCache(ctx context.Context, basketAddr string) (*basketStateCache, error) {
	var c basketStateCache
	var needsRebal int

	err := h.db.QueryRow(`
		SELECT basket_address, constituents_json, current_weights_json, balances_json,
		       total_value_usdg, nav_per_token, max_drift_bps, needs_rebalancing, cached_at
		FROM basket_state_cache WHERE basket_address = ?`, basketAddr).Scan(
		&c.BasketAddress, &c.ConstituentsJSON, &c.CurrentWeightsJSON, &c.BalancesJSON,
		&c.TotalValueUsdg, &c.NavPerToken, &c.MaxDriftBps, &needsRebal, &c.CachedAt,
	)
	c.NeedsRebalancing = needsRebal == 1

	if err == nil && time.Now().Unix()-c.CachedAt <= 30 {
		return &c, nil
	}

	fresh, err := h.fetchBasketStateRPC(ctx, basketAddr)
	if err != nil {
		if c.BasketAddress != "" {
			return &c, nil
		}
		return nil, err
	}

	h.writeBasketStateCache(fresh)
	return fresh, nil
}

func (h *handler) fetchBasketStateRPC(ctx context.Context, basketAddr string) (*basketStateCache, error) {
	if h.rpcClient == nil {
		return nil, fmt.Errorf("RPC client not available")
	}

	addr := common.HexToAddress(basketAddr)

	callCtx, callCancel := context.WithTimeout(ctx, rpcTimeout)
	defer callCancel()

	data, err := h.rpcClient.CallContract(callCtx, ethereum.CallMsg{
		To:   &addr,
		Data: basketStateABI.Methods["basketState"].ID,
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("basketState call: %w", err)
	}

	unpacked, err := basketStateABI.Methods["basketState"].Outputs.Unpack(data)
	if err != nil || len(unpacked) < 9 {
		return nil, fmt.Errorf("basketState unpack: %w", err)
	}

	constituents, _ := unpacked[0].([]common.Address)
	targetWeights, _ := unpacked[1].([]*big.Int)
	currentWeights, _ := unpacked[2].([]*big.Int)
	balances, _ := unpacked[3].([]*big.Int)
	totalValue, _ := unpacked[4].(*big.Int)
	nav, _ := unpacked[5].(*big.Int)
	rebalancingEnabled, _ := unpacked[6].(bool)
	driftThresholdBps, _ := unpacked[7].(*big.Int)
	maxDrift, _ := unpacked[8].(*big.Int)

	// needsRebalancing is true when rebalancing is enabled and the maximum
	// drift across all constituents meets or exceeds the basket's threshold.
	needsRebal := false
	if rebalancingEnabled && maxDrift != nil && driftThresholdBps != nil {
		needsRebal = maxDrift.Cmp(driftThresholdBps) >= 0
	}

	constituentAddrs := make([]string, len(constituents))
	for i, c := range constituents {
		constituentAddrs[i] = strings.ToLower(c.Hex())
	}

	type assetMeta struct {
		symbol string
		name   string
		sector string
	}
	metaByAddr := make(map[string]assetMeta, len(constituentAddrs))

	if len(constituentAddrs) > 0 {
		placeholders := make([]string, len(constituentAddrs))
		args := make([]any, len(constituentAddrs))
		for i, a := range constituentAddrs {
			placeholders[i] = "?"
			args[i] = a
		}
		inClause := strings.Join(placeholders, ",")

		metaRows, err := h.db.Query(
			`SELECT address, symbol, name, sector FROM supported_assets WHERE address IN (`+inClause+`)`,
			args...,
		)
		if err == nil {
			defer metaRows.Close()
			for metaRows.Next() {
				var a, sym, nm, sec string
				if metaRows.Scan(&a, &sym, &nm, &sec) == nil {
					metaByAddr[a] = assetMeta{symbol: sym, name: nm, sector: sec}
				}
			}
		}

		type priceInfo struct {
			currentPrice string
			change24h    string
		}
		priceByAddr := make(map[string]priceInfo, len(constituentAddrs))

		priceRows, err := h.db.Query(`
			SELECT
				p.stock_address,
				p.price_usdg AS current_price,
				COALESCE(
					ROUND(
						(CAST(p.price_usdg AS REAL) - CAST(prev.price_usdg AS REAL))
						/ CAST(prev.price_usdg AS REAL) * 100,
						2
					),
					0.0
				) AS change_24h
			FROM price_history p
			LEFT JOIN price_history prev
				ON prev.stock_address = p.stock_address
				AND prev.timestamp = (
					SELECT MAX(timestamp)
					FROM price_history
					WHERE stock_address = p.stock_address
					AND timestamp <= strftime('%s','now') - 86400
				)
			WHERE p.stock_address IN (`+inClause+`)
			AND p.timestamp = (
				SELECT MAX(timestamp)
				FROM price_history
				WHERE stock_address = p.stock_address
			)`, args...,
		)
		if err == nil {
			defer priceRows.Close()
			for priceRows.Next() {
				var a, cur string
				var chg float64
				if priceRows.Scan(&a, &cur, &chg) == nil {
					priceByAddr[a] = priceInfo{
						currentPrice: cur,
						change24h:    fmt.Sprintf("%.2f", chg),
					}
				}
			}
		}

		details := make([]constituentDetail, 0, len(constituents))
		for i, c := range constituents {
			cAddr := strings.ToLower(c.Hex())
			meta := metaByAddr[cAddr]
			price := priceByAddr[cAddr]

			tw, cw, bal := "0", "0", "0"
			if i < len(targetWeights) && targetWeights[i] != nil {
				tw = targetWeights[i].String()
			}
			if i < len(currentWeights) && currentWeights[i] != nil {
				cw = currentWeights[i].String()
			}
			if i < len(balances) && balances[i] != nil {
				bal = balances[i].String()
			}

			valueUsdg := "0"
			if price.currentPrice != "" && bal != "0" {
				balBig := new(big.Int)
				priceBig := new(big.Int)
				if _, ok := balBig.SetString(bal, 10); ok {
					if _, ok := priceBig.SetString(price.currentPrice, 10); ok {
						val := new(big.Int).Mul(balBig, priceBig)
						val.Div(val, new(big.Int).Exp(big.NewInt(10), big.NewInt(20), nil))
						valueUsdg = val.String()
					}
				}
			}

			currentPriceStr := price.currentPrice
			if currentPriceStr == "" {
				currentPriceStr = "0"
			}
			changeStr := price.change24h
			if changeStr == "" {
				changeStr = "0.00"
			}

			details = append(details, constituentDetail{
				Address:           cAddr,
				Symbol:            meta.symbol,
				Name:              meta.name,
				Sector:            meta.sector,
				TargetWeightBps:   tw,
				CurrentWeightBps:  cw,
				BalanceRaw:        bal,
				PriceUsdg:         currentPriceStr,
				ValueUsdg:         valueUsdg,
				PriceChange24hPct: changeStr,
			})
		}

		consJSON, _ := json.Marshal(details)
		cwJSON, _ := json.Marshal(bigIntSliceToStrings(currentWeights))
		balJSON, _ := json.Marshal(bigIntSliceToStrings(balances))

		navStr, tvStr, maxDriftVal := "0", "0", int64(0)
		if nav != nil {
			navStr = nav.String()
		}
		if totalValue != nil {
			tvStr = totalValue.String()
		}
		if maxDrift != nil {
			maxDriftVal = maxDrift.Int64()
		}

		return &basketStateCache{
			BasketAddress:      basketAddr,
			ConstituentsJSON:   string(consJSON),
			CurrentWeightsJSON: string(cwJSON),
			BalancesJSON:       string(balJSON),
			TotalValueUsdg:     tvStr,
			NavPerToken:        navStr,
			MaxDriftBps:        maxDriftVal,
			NeedsRebalancing:   needsRebal,
			CachedAt:           time.Now().Unix(),
		}, nil
	}

	return &basketStateCache{
		BasketAddress:    basketAddr,
		ConstituentsJSON: "[]",
		TotalValueUsdg:   "0",
		NavPerToken:      "0",
		CachedAt:         time.Now().Unix(),
	}, nil
}

type constituentDetail struct {
	Address           string `json:"address"`
	Symbol            string `json:"symbol"`
	Name              string `json:"name"`
	Sector            string `json:"sector"`
	TargetWeightBps   string `json:"targetWeightBps"`
	CurrentWeightBps  string `json:"currentWeightBps"`
	BalanceRaw        string `json:"balanceRaw"`
	PriceUsdg         string `json:"priceUsdg"`
	ValueUsdg         string `json:"valueUsdg"`
	PriceChange24hPct string `json:"priceChange24hPct"`
}

func (h *handler) writeBasketStateCache(c *basketStateCache) {
	needsRebal := 0
	if c.NeedsRebalancing {
		needsRebal = 1
	}
	_, err := h.db.Exec(`
		INSERT INTO basket_state_cache
			(basket_address, constituents_json, current_weights_json, balances_json,
			 total_value_usdg, nav_per_token, max_drift_bps, needs_rebalancing, cached_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(basket_address) DO UPDATE SET
			constituents_json    = excluded.constituents_json,
			current_weights_json = excluded.current_weights_json,
			balances_json        = excluded.balances_json,
			total_value_usdg     = excluded.total_value_usdg,
			nav_per_token        = excluded.nav_per_token,
			max_drift_bps        = excluded.max_drift_bps,
			needs_rebalancing    = excluded.needs_rebalancing,
			cached_at            = excluded.cached_at`,
		c.BasketAddress, c.ConstituentsJSON, c.CurrentWeightsJSON, c.BalancesJSON,
		c.TotalValueUsdg, c.NavPerToken, c.MaxDriftBps, needsRebal, c.CachedAt,
	)
	if err != nil {
		log.Printf("api: writeBasketStateCache(%s): %v", c.BasketAddress, err)
	}
}

func (h *handler) navChangePct(basketAddr string, windowSecs int64) string {
	var current, prior string

	h.db.QueryRow(`
		SELECT nav_per_token FROM nav_history
		WHERE basket_address = ? ORDER BY timestamp DESC LIMIT 1`, basketAddr,
	).Scan(&current)

	h.db.QueryRow(`
		SELECT nav_per_token FROM nav_history
		WHERE basket_address = ? AND timestamp <= ?
		ORDER BY timestamp DESC LIMIT 1`,
		basketAddr, time.Now().Unix()-windowSecs,
	).Scan(&prior)

	return pctChange(prior, current)
}

// Catalogue

func (h *handler) getCatalogue(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.Query(`
		SELECT
			a.address, a.symbol, a.name, a.sector, a.oracle_address, a.is_active,
			COALESCE(ph.price_usdg,    '0') AS current_price,
			COALESCE(ph24.price_usdg,  '0') AS price_24h_ago
		FROM supported_assets a
		LEFT JOIN (
			SELECT stock_address, price_usdg
			FROM price_history p1
			WHERE timestamp = (
				SELECT MAX(timestamp) FROM price_history p2
				WHERE p2.stock_address = p1.stock_address
			)
		) ph ON ph.stock_address = a.address
		LEFT JOIN (
			SELECT stock_address, price_usdg
			FROM price_history p1
			WHERE timestamp = (
				SELECT MAX(timestamp) FROM price_history p2
				WHERE p2.stock_address = p1.stock_address
				AND p2.timestamp <= strftime('%s','now') - 86400
			)
		) ph24 ON ph24.stock_address = a.address
		ORDER BY a.symbol ASC`)
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var assets []Asset
	for rows.Next() {
		var a Asset
		var active int
		var price24hAgo string

		if err := rows.Scan(
			&a.Address, &a.Symbol, &a.Name, &a.Sector,
			&a.Oracle, &active, &a.CurrentPrice, &price24hAgo,
		); err != nil {
			continue
		}

		a.Active = active == 1
		a.PriceChange24hPct = pctChange(price24hAgo, a.CurrentPrice)
		assets = append(assets, a)
	}

	if assets == nil {
		assets = []Asset{}
	}

	jsonOK(w, assets)
}

type Asset struct {
	Address           string `json:"address"`
	Symbol            string `json:"symbol"`
	Name              string `json:"name"`
	Sector            string `json:"sector"`
	Oracle            string `json:"oracle"`
	Active            bool   `json:"isActive"`
	CurrentPrice      string `json:"currentPriceUsdg"`
	PriceChange24hPct string `json:"priceChange24hPct"`
}

func (h *handler) getCatalogueAsset(w http.ResponseWriter, r *http.Request) {
	addr := strings.ToLower(r.PathValue("address"))
	var a struct {
		Address           string `json:"address"`
		Symbol            string `json:"symbol"`
		Name              string `json:"name"`
		Sector            string `json:"sector"`
		Oracle            string `json:"oracle"`
		Active            bool   `json:"isActive"`
		CurrentPriceUsdg  string `json:"currentPriceUsdg"`
		PriceChange24hPct string `json:"priceChange24hPct"`
	}
	var active int
	err := h.db.QueryRow(`
		SELECT address, symbol, name, sector, oracle_address, is_active
		FROM supported_assets WHERE address = ?`, addr).Scan(
		&a.Address, &a.Symbol, &a.Name, &a.Sector, &a.Oracle, &active,
	)
	if err == sql.ErrNoRows {
		jsonError(w, "asset not found", http.StatusNotFound)
		return
	}
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	a.Active = active == 1

	var currentPrice, prev24h string
	h.db.QueryRow(`
		SELECT price_usdg FROM price_history
		WHERE stock_address = ? ORDER BY timestamp DESC LIMIT 1`, addr,
	).Scan(&currentPrice)
	h.db.QueryRow(`
		SELECT price_usdg FROM price_history
		WHERE stock_address = ?
		AND timestamp <= strftime('%s','now') - 86400
		ORDER BY timestamp DESC LIMIT 1`, addr,
	).Scan(&prev24h)

	a.CurrentPriceUsdg = currentPrice
	if a.CurrentPriceUsdg == "" {
		a.CurrentPriceUsdg = "0"
	}
	a.PriceChange24hPct = pctChange(prev24h, currentPrice)

	jsonOK(w, a)
}

func (h *handler) getPrices(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.Query(`
		SELECT
			ph.stock_address,
			ph.price_usdg  AS current_price,
			COALESCE(ph24.price_usdg, '0') AS price_24h_ago
		FROM (
			SELECT stock_address, price_usdg, MAX(timestamp) AS ts
			FROM price_history GROUP BY stock_address
		) ph
		LEFT JOIN (
			SELECT stock_address, price_usdg
			FROM price_history p1
			WHERE timestamp = (
				SELECT MAX(timestamp) FROM price_history p2
				WHERE p2.stock_address = p1.stock_address
				AND p2.timestamp <= strftime('%s','now') - 86400
			)
		) ph24 ON ph24.stock_address = ph.stock_address`)
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type Price struct {
		Address           string `json:"address"`
		Price             string `json:"priceUsdg"`
		PriceChange24hPct string `json:"priceChange24hPct"`
	}

	var prices []Price
	for rows.Next() {
		var p Price
		var price24hAgo string
		if err := rows.Scan(&p.Address, &p.Price, &price24hAgo); err != nil {
			continue
		}
		p.PriceChange24hPct = pctChange(price24hAgo, p.Price)
		prices = append(prices, p)
	}

	if prices == nil {
		prices = []Price{}
	}

	jsonOK(w, prices)
}

// Positions

func (h *handler) getBasketPerformance(w http.ResponseWriter, r *http.Request) {
	addr := strings.ToLower(r.PathValue("address"))

	rows, err := h.db.Query(`
		SELECT nav_per_token, total_value_usdg, timestamp
		FROM nav_history WHERE basket_address = ?
		ORDER BY timestamp ASC`, addr)
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type Point struct {
		NavPerToken    string `json:"navPerToken"`
		TotalValueUsdg string `json:"totalValueUsdg"`
		Timestamp      int64  `json:"timestamp"`
	}

	var points []Point
	for rows.Next() {
		var p Point
		if err := rows.Scan(&p.NavPerToken, &p.TotalValueUsdg, &p.Timestamp); err != nil {
			continue
		}
		points = append(points, p)
	}

	if points == nil {
		points = []Point{}
	}

	jsonOK(w, points)
}

func (h *handler) getPosition(w http.ResponseWriter, r *http.Request) {
	basketAddr := strings.ToLower(r.PathValue("address"))
	wallet := strings.ToLower(r.PathValue("wallet"))

	var basketName, basketSymbol, navStr string
	h.db.QueryRow(`
		SELECT b.name, b.symbol, COALESCE(n.nav_per_token, '0')
		FROM baskets b
		LEFT JOIN (
			SELECT basket_address, nav_per_token FROM nav_history
			WHERE basket_address = ? ORDER BY timestamp DESC LIMIT 1
		) n ON n.basket_address = b.address
		WHERE b.address = ?`, basketAddr, basketAddr,
	).Scan(&basketName, &basketSymbol, &navStr)

	constituents := []constituentSummary{}
	if cRows, err := h.db.Query(`
		SELECT bc.stock_address, bc.symbol, bc.target_weight_bps, COALESCE(sa.sector,'')
		FROM basket_constituents bc
		LEFT JOIN supported_assets sa ON sa.address = bc.stock_address
		WHERE bc.basket_address = ?
		ORDER BY bc.display_order`, basketAddr); err == nil {
		defer cRows.Close()
		for cRows.Next() {
			var c constituentSummary
			if cRows.Scan(&c.Address, &c.Symbol, &c.TargetWeightBps, &c.Sector) == nil {
				constituents = append(constituents, c)
			}
		}
	}

	var totalDeposited, totalRedeemed string
	h.db.QueryRow(`
		SELECT COALESCE(SUM(CAST(usdg_amount AS REAL)), 0)
		FROM deposits WHERE basket_address = ? AND investor_address = ?`,
		basketAddr, wallet,
	).Scan(&totalDeposited)
	h.db.QueryRow(`
		SELECT COALESCE(SUM(CAST(usdg_returned AS REAL)), 0)
		FROM redemptions WHERE basket_address = ? AND investor_address = ?`,
		basketAddr, wallet,
	).Scan(&totalRedeemed)

	dep, _ := new(big.Float).SetString(totalDeposited)
	red, _ := new(big.Float).SetString(totalRedeemed)
	if dep == nil {
		dep = new(big.Float)
	}
	if red == nil {
		red = new(big.Float)
	}

	var tokensMinted, tokensBurned string
	h.db.QueryRow(`
		SELECT COALESCE(SUM(CAST(basket_tokens_minted AS REAL)), 0)
		FROM deposits WHERE basket_address = ? AND investor_address = ?`,
		basketAddr, wallet,
	).Scan(&tokensMinted)
	h.db.QueryRow(`
		SELECT COALESCE(SUM(CAST(basket_tokens_burned AS REAL)), 0)
		FROM redemptions WHERE basket_address = ? AND investor_address = ?`,
		basketAddr, wallet,
	).Scan(&tokensBurned)

	minted, _ := new(big.Float).SetString(tokensMinted)
	burned, _ := new(big.Float).SetString(tokensBurned)
	if minted == nil {
		minted = new(big.Float)
	}
	if burned == nil {
		burned = new(big.Float)
	}

	balance := new(big.Float).Sub(minted, burned)
	nav, _ := new(big.Float).SetString(navStr)
	if nav == nil {
		nav = new(big.Float)
	}

	currentValue := new(big.Float).Mul(balance, nav)
	currentValue.Quo(currentValue, new(big.Float).SetFloat64(1e18))

	costBasis := new(big.Float).Sub(dep, red)
	pnl := new(big.Float).Sub(currentValue, costBasis)
	pnlPct := "0.00"
	if costBasis.Sign() > 0 {
		p := new(big.Float).Quo(pnl, costBasis)
		p.Mul(p, new(big.Float).SetFloat64(100))
		pnlPct = p.Text('f', 2)
	}

	jsonOK(w, map[string]any{
		"basketAddress":      basketAddr,
		"walletAddress":      wallet,
		"basketName":         basketName,
		"basketSymbol":       basketSymbol,
		"basketNavPerToken":  navStr,
		"basketTokenBalance": balance.Text('f', 0),
		"currentValueUsdg":   currentValue.Text('f', 0),
		"totalDepositedUsdg": dep.Text('f', 0),
		"unrealisedPnlUsdg":  pnl.Text('f', 0),
		"unrealisedPnlPct":   pnlPct,
		"constituents":       constituents,
	})
}

type basketAgg struct {
	name               string
	symbol             string
	navPerToken        string
	rebalancingEnabled bool
	suspended          bool
	totalDeposited     *big.Float
	totalRedeemed      *big.Float
	tokensMinted       *big.Float
	tokensBurned       *big.Float
}

func (h *handler) getPortfolio(w http.ResponseWriter, r *http.Request) {
	wallet := strings.ToLower(r.PathValue("wallet"))

	agg := make(map[string]*basketAgg)
	var order []string

	depRows, err := h.db.Query(`
		SELECT d.basket_address,
		       b.name, b.symbol, b.rebalancing_enabled, b.suspended,
		       COALESCE(n.nav_per_token,'0'),
		       SUM(CAST(d.usdg_amount AS REAL)),
		       SUM(CAST(d.basket_tokens_minted AS REAL))
		FROM deposits d
		JOIN baskets b ON b.address = d.basket_address
		LEFT JOIN (
			SELECT basket_address, nav_per_token
			FROM nav_history
			WHERE (basket_address, timestamp) IN (
				SELECT basket_address, MAX(timestamp)
				FROM nav_history GROUP BY basket_address
			)
		) n ON n.basket_address = d.basket_address
		WHERE d.investor_address = ?
		GROUP BY d.basket_address`, wallet)
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	defer depRows.Close()

	for depRows.Next() {
		var addr, name, symbol, nav string
		var rebal, susp int
		var dep, minted float64
		if depRows.Scan(&addr, &name, &symbol, &rebal, &susp, &nav, &dep, &minted) != nil {
			continue
		}
		agg[addr] = &basketAgg{
			name:               name,
			symbol:             symbol,
			navPerToken:        nav,
			rebalancingEnabled: rebal == 1,
			suspended:          susp == 1,
			totalDeposited:     new(big.Float).SetFloat64(dep),
			totalRedeemed:      new(big.Float),
			tokensMinted:       new(big.Float).SetFloat64(minted),
			tokensBurned:       new(big.Float),
		}
		order = append(order, addr)
	}

	redRows, err := h.db.Query(`
		SELECT basket_address,
		       SUM(CAST(usdg_returned AS REAL)),
		       SUM(CAST(basket_tokens_burned AS REAL))
		FROM redemptions
		WHERE investor_address = ?
		GROUP BY basket_address`, wallet)
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	defer redRows.Close()

	for redRows.Next() {
		var addr string
		var red, burned float64
		if redRows.Scan(&addr, &red, &burned) != nil {
			continue
		}
		if a, ok := agg[addr]; ok {
			a.totalRedeemed = new(big.Float).SetFloat64(red)
			a.tokensBurned = new(big.Float).SetFloat64(burned)
		}
	}

	constituentsByBasket := make(map[string][]constituentSummary)
	if len(order) > 0 {
		placeholders := make([]string, len(order))
		args := make([]any, len(order))
		for i, addr := range order {
			placeholders[i] = "?"
			args[i] = addr
		}
		if cRows, err := h.db.Query(`
			SELECT bc.basket_address, bc.stock_address, bc.symbol,
			       bc.target_weight_bps, COALESCE(sa.sector,'')
			FROM basket_constituents bc
			LEFT JOIN supported_assets sa ON sa.address = bc.stock_address
			WHERE bc.basket_address IN (`+strings.Join(placeholders, ",")+`)
			ORDER BY bc.basket_address, bc.display_order`, args...); err == nil {
			defer cRows.Close()
			for cRows.Next() {
				var basketAddr string
				var c constituentSummary
				if cRows.Scan(&basketAddr, &c.Address, &c.Symbol, &c.TargetWeightBps, &c.Sector) == nil {
					constituentsByBasket[basketAddr] = append(constituentsByBasket[basketAddr], c)
				}
			}
		}
	}

	positions := make([]Position, 0, len(order))
	totalValue := new(big.Float)
	totalDeposit := new(big.Float)

	for _, addr := range order {
		a := agg[addr]

		bal := new(big.Float).Sub(a.tokensMinted, a.tokensBurned)
		navF, _ := new(big.Float).SetString(a.navPerToken)
		if navF == nil {
			navF = new(big.Float)
		}

		currentVal := new(big.Float).Mul(bal, navF)
		currentVal.Quo(currentVal, new(big.Float).SetFloat64(1e18))

		costBasis := new(big.Float).Sub(a.totalDeposited, a.totalRedeemed)
		pnl := new(big.Float).Sub(currentVal, costBasis)

		pnlPct := "0.00"
		if costBasis.Sign() > 0 {
			p := new(big.Float).Quo(pnl, costBasis)
			p.Mul(p, new(big.Float).SetFloat64(100))
			pnlPct = p.Text('f', 2)
		}

		totalValue.Add(totalValue, currentVal)
		totalDeposit.Add(totalDeposit, a.totalDeposited)

		consts := constituentsByBasket[addr]
		if consts == nil {
			consts = []constituentSummary{}
		}

		positions = append(positions, Position{
			BasketAddress:      addr,
			BasketName:         a.name,
			BasketSymbol:       a.symbol,
			BasketNavPerToken:  a.navPerToken,
			RebalancingEnabled: a.rebalancingEnabled,
			Suspended:          a.suspended,
			BasketTokenBalance: bal.Text('f', 0),
			CurrentValueUsdg:   currentVal.Text('f', 0),
			TotalDepositedUsdg: a.totalDeposited.Text('f', 0),
			UnrealisedPnlUsdg:  pnl.Text('f', 0),
			UnrealisedPnlPct:   pnlPct,
			Constituents:       consts,
		})
	}

	totalPnl := new(big.Float).Sub(totalValue, totalDeposit)
	totalPnlPct := "0.00"
	if totalDeposit.Sign() > 0 {
		p := new(big.Float).Quo(totalPnl, totalDeposit)
		p.Mul(p, new(big.Float).SetFloat64(100))
		totalPnlPct = p.Text('f', 2)
	}

	jsonOK(w, map[string]any{
		"walletAddress":          wallet,
		"totalValueUsdg":         totalValue.Text('f', 0),
		"totalDepositedUsdg":     totalDeposit.Text('f', 0),
		"totalUnrealisedPnlUsdg": totalPnl.Text('f', 0),
		"totalUnrealisedPnlPct":  totalPnlPct,
		"positions":              positions,
	})
}

type Position struct {
	BasketAddress      string               `json:"basketAddress"`
	BasketName         string               `json:"basketName"`
	BasketSymbol       string               `json:"basketSymbol"`
	BasketNavPerToken  string               `json:"basketNavPerToken"`
	RebalancingEnabled bool                 `json:"rebalancingEnabled"`
	Suspended          bool                 `json:"suspended"`
	BasketTokenBalance string               `json:"basketTokenBalance"`
	CurrentValueUsdg   string               `json:"currentValueUsdg"`
	TotalDepositedUsdg string               `json:"totalDepositedUsdg"`
	UnrealisedPnlUsdg  string               `json:"unrealisedPnlUsdg"`
	UnrealisedPnlPct   string               `json:"unrealisedPnlPct"`
	Constituents       []constituentSummary `json:"constituents"`
}

// Creator

type BasketEntry struct {
	BasketAddress      string          `json:"basketAddress"`
	BasketName         string          `json:"basketName"`
	BasketSymbol       string          `json:"basketSymbol"`
	CreatorToken       string          `json:"creatorTokenAddress"`
	TotalValueUsdg     string          `json:"totalValueUsdg"`
	TotalClaimableUsdg string          `json:"totalClaimableUsdg"`
	UnclaimedSnapshots []snapshotEntry `json:"unclaimedSnapshots"`
	RevenueHistory     []snapshotEntry `json:"revenueHistory"`
	ClaimHistory       []ClaimEntry    `json:"claimHistory"`
}

type ClaimEntry struct {
	Claimer    string `json:"claimer"`
	SnapshotID int64  `json:"snapshotId"`
	UsdgAmount string `json:"usdgAmount"`
	Timestamp  int64  `json:"timestamp"`
	TxHash     string `json:"txHash"`
}

func (h *handler) getCreatorDashboard(w http.ResponseWriter, r *http.Request) {
	wallet := strings.ToLower(r.PathValue("wallet"))

	basketRows, err := h.db.Query(`
		SELECT b.address, b.creator_token_address, b.name, b.symbol,
		       COALESCE(n.total_value_usdg,'0')
		FROM baskets b
		LEFT JOIN (
			SELECT basket_address, total_value_usdg
			FROM nav_history
			WHERE (basket_address, timestamp) IN (
				SELECT basket_address, MAX(timestamp)
				FROM nav_history GROUP BY basket_address
			)
		) n ON n.basket_address = b.address
		WHERE b.creator_address = ?
		ORDER BY b.created_at DESC`, wallet)
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	defer basketRows.Close()

	type basketMeta struct {
		address      string
		creatorToken string
		name         string
		symbol       string
		totalValue   string
	}

	var basketOrder []string
	baskets := make(map[string]*basketMeta)

	for basketRows.Next() {
		var m basketMeta
		if basketRows.Scan(&m.address, &m.creatorToken, &m.name, &m.symbol, &m.totalValue) == nil {
			basketOrder = append(basketOrder, m.address)
			baskets[m.address] = &m
		}
	}

	if len(basketOrder) == 0 {
		jsonOK(w, map[string]any{
			"walletAddress":      wallet,
			"totalClaimableUsdg": "0",
			"baskets":            []any{},
		})
		return
	}

	placeholders := make([]string, len(basketOrder))
	args := make([]any, len(basketOrder))
	for i, addr := range basketOrder {
		placeholders[i] = "?"
		args[i] = addr
	}

	snapRows, err := h.db.Query(
		`SELECT basket_address, snapshot_id, usdg_amount, timestamp, tx_hash
		 FROM fee_snapshots
		 WHERE basket_address IN (`+strings.Join(placeholders, ",")+`)
		 ORDER BY basket_address, snapshot_id ASC`,
		args...,
	)

	snapshotsByBasket := make(map[string][]snapshotEntry)
	if err == nil {
		defer snapRows.Close()
		for snapRows.Next() {
			var bAddr string
			var s snapshotEntry
			if snapRows.Scan(&bAddr, &s.SnapshotID, &s.UsdgAmount, &s.Timestamp, &s.TxHash) == nil {
				snapshotsByBasket[bAddr] = append(snapshotsByBasket[bAddr], s)
			}
		}
	}

	claimsByBasket := make(map[string][]ClaimEntry)
	claimRows, err := h.db.Query(
		`SELECT basket_address, claimer_address, snapshot_id, usdg_amount, timestamp, tx_hash
     FROM revenue_claims
     WHERE basket_address IN (`+strings.Join(placeholders, ",")+`)
     ORDER BY basket_address, timestamp DESC`,
		args...,
	)
	if err == nil {
		defer claimRows.Close()
		for claimRows.Next() {
			var bAddr string
			var c ClaimEntry
			if claimRows.Scan(&bAddr, &c.Claimer, &c.SnapshotID, &c.UsdgAmount, &c.Timestamp, &c.TxHash) == nil {
				claimsByBasket[bAddr] = append(claimsByBasket[bAddr], c)
			}
		}
	}

	totalClaimable := new(big.Int)
	result := make([]BasketEntry, 0, len(basketOrder))

	for _, addr := range basketOrder {
		m := baskets[addr]
		snaps := snapshotsByBasket[addr]
		if snaps == nil {
			snaps = []snapshotEntry{}
		}

		unclaimed := h.getClaimableSnapshots(r.Context(), wallet, addr, m.creatorToken, snaps)

		basketClaimable := new(big.Int)
		for _, s := range unclaimed {
			if amt, ok := new(big.Int).SetString(s.ClaimableUsdg, 10); ok {
				basketClaimable.Add(basketClaimable, amt)
			}
		}
		totalClaimable.Add(totalClaimable, basketClaimable)

		claims := claimsByBasket[addr]
		if claims == nil {
			claims = []ClaimEntry{}
		}

		result = append(result, BasketEntry{
			BasketAddress:      addr,
			BasketName:         m.name,
			BasketSymbol:       m.symbol,
			CreatorToken:       m.creatorToken,
			TotalValueUsdg:     m.totalValue,
			TotalClaimableUsdg: basketClaimable.String(),
			UnclaimedSnapshots: unclaimed,
			RevenueHistory:     snaps,
			ClaimHistory:       claims,
		})
	}

	jsonOK(w, map[string]any{
		"walletAddress":      wallet,
		"totalClaimableUsdg": totalClaimable.String(),
		"baskets":            result,
	})
}

// getClaimableSnapshots returns claimable amounts per snapshot, reading from
// creator_claimable_cache if fresh (< 60s), otherwise calling claimableRevenue()
// on the contract via the shared RPC client.
func (h *handler) getClaimableSnapshots(
	ctx context.Context,
	wallet, basketAddr, creatorTokenAddr string,
	snapshots []snapshotEntry,
) []snapshotEntry {
	if len(snapshots) == 0 {
		return []snapshotEntry{}
	}

	now := time.Now().Unix()

	type cachedEntry struct {
		claimableUsdg string
		cachedAt      int64
	}
	cache := make(map[int64]cachedEntry)

	cacheRows, err := h.db.Query(`
		SELECT snapshot_id, claimable_usdg, cached_at
		FROM creator_claimable_cache
		WHERE wallet_address = ? AND basket_address = ?`,
		wallet, basketAddr,
	)
	if err == nil {
		defer cacheRows.Close()
		for cacheRows.Next() {
			var snapID int64
			var ce cachedEntry
			if cacheRows.Scan(&snapID, &ce.claimableUsdg, &ce.cachedAt) == nil {
				cache[snapID] = ce
			}
		}
	}

	ctAddr := common.HexToAddress(creatorTokenAddr)
	walletAddr := common.HexToAddress(wallet)
	result := make([]snapshotEntry, 0, len(snapshots))

	for _, snap := range snapshots {
		if ce, ok := cache[snap.SnapshotID]; ok && now-ce.cachedAt <= 60 {
			snap.ClaimableUsdg = ce.claimableUsdg
			result = append(result, snap)
			continue
		}

		claimable := "0"
		rpcSucceeded := false

		if h.rpcClient != nil {
			input, err := claimableRevenueABI.Pack("claimableRevenue",
				walletAddr,
				new(big.Int).SetInt64(snap.SnapshotID),
			)
			if err == nil {
				callCtx, callCancel := context.WithTimeout(ctx, rpcTimeout)
				data, err := h.rpcClient.CallContract(callCtx, ethereum.CallMsg{
					To:   &ctAddr,
					Data: input,
				}, nil)
				callCancel()

				if err != nil {
					log.Printf("api: claimableRevenue RPC snapshot=%d wallet=%s: %v", snap.SnapshotID, wallet, err)
				} else {
					unpacked, err := claimableRevenueABI.Methods["claimableRevenue"].Outputs.Unpack(data)
					if err == nil && len(unpacked) > 0 {
						if amount, ok := unpacked[0].(*big.Int); ok && amount != nil {
							claimable = amount.String()
							rpcSucceeded = true
						}
					}
				}
			}
		}

		// Only write to cache on RPC success. Caching a zero on RPC failure
		// would poison the cache and hide real claimable amounts.
		if rpcSucceeded {
			h.db.Exec(`
				INSERT INTO creator_claimable_cache
					(wallet_address, snapshot_id, basket_address, claimable_usdg, cached_at)
				VALUES (?, ?, ?, ?, ?)
				ON CONFLICT(wallet_address, snapshot_id, basket_address) DO UPDATE SET
					claimable_usdg = excluded.claimable_usdg,
					cached_at      = excluded.cached_at`,
				wallet, snap.SnapshotID, basketAddr, claimable, now,
			)
		}

		snap.ClaimableUsdg = claimable
		result = append(result, snap)
	}

	return result
}

func (h *handler) getCreatorToken(w http.ResponseWriter, r *http.Request) {
	addr := strings.ToLower(r.PathValue("address"))

	rows, err := h.db.Query(`
		SELECT snapshot_id, usdg_amount, timestamp, tx_hash
		FROM fee_snapshots
		WHERE basket_address IN (
			SELECT address FROM baskets WHERE creator_token_address = ?
		)
		ORDER BY snapshot_id ASC`, addr)
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type Snapshot struct {
		SnapshotID int64  `json:"snapshotId"`
		UsdgAmount string `json:"usdgAmount"`
		Timestamp  int64  `json:"timestamp"`
		TxHash     string `json:"txHash"`
	}

	var snapshots []Snapshot
	totalRevenue := new(big.Int)

	for rows.Next() {
		var s Snapshot
		if err := rows.Scan(&s.SnapshotID, &s.UsdgAmount, &s.Timestamp, &s.TxHash); err != nil {
			continue
		}
		if amt, ok := new(big.Int).SetString(s.UsdgAmount, 10); ok {
			totalRevenue.Add(totalRevenue, amt)
		}
		snapshots = append(snapshots, s)
	}

	if snapshots == nil {
		snapshots = []Snapshot{}
	}

	jsonOK(w, map[string]any{
		"creatorTokenAddress": addr,
		"totalRevenueUsdg":    totalRevenue.String(),
		"snapshots":           snapshots,
	})
}

// AI Compose

func (h *handler) aiCompose(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Thesis string `json:"thesis"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 32768)).Decode(&req); err != nil || len(req.Thesis) < 20 {
		jsonError(w, "thesis must be at least 20 characters", http.StatusBadRequest)
		return
	}

	rows, err := h.db.Query(`
		SELECT a.address, a.symbol, a.name, a.sector,
		       COALESCE(p.price_usdg,'0') AS price
		FROM supported_assets a
		LEFT JOIN (
			SELECT stock_address, price_usdg
			FROM price_history p1
			WHERE timestamp = (
				SELECT MAX(timestamp) FROM price_history p2
				WHERE p2.stock_address = p1.stock_address
			)
		) p ON p.stock_address = a.address
		WHERE a.is_active = 1`)
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var catalogue []ai.CatalogueAsset
	for rows.Next() {
		var a ai.CatalogueAsset
		if rows.Scan(&a.Address, &a.Symbol, &a.Name, &a.Sector, &a.CurrentPriceUsdg) == nil {
			catalogue = append(catalogue, a)
		}
	}

	if len(catalogue) < 3 {
		jsonError(w, "insufficient active assets in catalogue (minimum 3 required)", http.StatusServiceUnavailable)
		return
	}

	composer := ai.NewComposer(h.openAIKey, h.openAIModel)
	proposal, err := composer.Compose(r.Context(), req.Thesis, catalogue)
	if err != nil {
		log.Printf("api: ai compose error: %v", err)
		jsonError(w, err.Error(), http.StatusBadGateway)
		return
	}

	jsonOK(w, proposal)
}

// Helpers

func jsonOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(v)
}

func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{
		"error": msg,
	})
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start))
	})
}

func pctChange(prior, current string) string {
	p, ok1 := new(big.Float).SetString(prior)
	c, ok2 := new(big.Float).SetString(current)
	if !ok1 || !ok2 || p == nil || c == nil || p.Sign() == 0 {
		return "0.00"
	}
	diff := new(big.Float).Sub(c, p)
	pct := new(big.Float).Quo(diff, p)
	pct.Mul(pct, new(big.Float).SetFloat64(100))
	return pct.Text('f', 2)
}

func bigIntSliceToStrings(s []*big.Int) []string {
	out := make([]string, len(s))
	for i, v := range s {
		if v != nil {
			out[i] = v.String()
		} else {
			out[i] = "0"
		}
	}
	return out
}

func (h *handler) serveOpenAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(openAPISpec))
}

func (h *handler) serveDocs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(swaggerHTML))
}
