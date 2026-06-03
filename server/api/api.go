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

// snapshotEntry is shared between getCreatorDashboard and getClaimableSnapshots.
type snapshotEntry struct {
	SnapshotID    int64  `json:"snapshotId"`
	UsdgAmount    string `json:"usdgAmount"`
	Timestamp     int64  `json:"timestamp"`
	TxHash        string `json:"txHash"`
	ClaimableUsdg string `json:"claimableByWallet"`
}

// NewRouter wires all HTTP routes.
func NewRouter(database *db.DB, openAIKey string, openAIModel string) http.Handler {
	mux := http.NewServeMux()

	h := &handler{
		db:          database,
		openAIKey:   openAIKey,
		openAIModel: openAIModel,
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
}

// ── Marketplace ───────────────────────────────────────────────────────────────

func (h *handler) listBaskets(w http.ResponseWriter, r *http.Request) {
	// Fetch all baskets with their latest NAV and 24h-ago NAV in one query.
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

	// Index by address for constituent attachment below.
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
	rows.Close()

	if len(byAddr) == 0 {
		jsonOK(w, []BasketSummary{})
		return
	}

	// Fetch all constituents for all baskets in one query, group in Go.
	cRows, err := h.db.Query(`
		SELECT bc.basket_address, bc.symbol, bc.target_weight_bps, COALESCE(sa.sector,'')
		FROM basket_constituents bc
		LEFT JOIN supported_assets sa ON sa.address = bc.stock_address
		ORDER BY bc.basket_address, bc.display_order`)
	if err == nil {
		for cRows.Next() {
			var basketAddr, sym, sector string
			var weight int64
			if cRows.Scan(&basketAddr, &sym, &weight, &sector) == nil {
				if b, ok := byAddr[basketAddr]; ok {
					b.Constituents = append(b.Constituents, constituentSummary{
						Symbol:          sym,
						TargetWeightBps: weight,
						Sector:          sector,
					})
					b.ConstituentCount = len(b.Constituents)
				}
			}
		}
		cRows.Close()
	}

	result := make([]BasketSummary, 0, len(order))
	for _, addr := range order {
		result = append(result, *byAddr[addr])
	}

	jsonOK(w, result)
}

type constituentSummary struct {
	Symbol          string `json:"symbol"`
	TargetWeightBps int64  `json:"targetWeightBps"`
	Sector          string `json:"sector"`
}

// ── Basket Detail ─────────────────────────────────────────────────────────────

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
	state, err := h.getBasketStateFromCache(addr)
	if err != nil {
		log.Printf("api: getBasketStateFromCache(%s): %v", addr, err)
	}

	// Performance history.
	perfRows, _ := h.db.Query(`
		SELECT nav_per_token, total_value_usdg, timestamp
		FROM nav_history WHERE basket_address = ?
		ORDER BY timestamp ASC`, addr)
	defer perfRows.Close()

	type PerfPoint struct {
		NavPerToken    string `json:"navPerToken"`
		TotalValueUsdg string `json:"totalValueUsdg"`
		Timestamp      int64  `json:"timestamp"`
	}
	var perf []PerfPoint
	for perfRows.Next() {
		var p PerfPoint
		if perfRows.Scan(&p.NavPerToken, &p.TotalValueUsdg, &p.Timestamp) == nil {
			perf = append(perf, p)
		}
	}
	if perf == nil {
		perf = []PerfPoint{}
	}

	// Rebalance history.
	rebRows, _ := h.db.Query(`
		SELECT timestamp, tx_hash, triggered_by FROM rebalances
		WHERE basket_address = ? ORDER BY timestamp DESC LIMIT 50`, addr)
	defer rebRows.Close()

	type RebalanceEntry struct {
		Timestamp   int64  `json:"timestamp"`
		TxHash      string `json:"txHash"`
		TriggeredBy string `json:"triggeredBy"`
	}
	var rebalHistory []RebalanceEntry
	for rebRows.Next() {
		var e RebalanceEntry
		if rebRows.Scan(&e.Timestamp, &e.TxHash, &e.TriggeredBy) == nil {
			rebalHistory = append(rebalHistory, e)
		}
	}
	if rebalHistory == nil {
		rebalHistory = []RebalanceEntry{}
	}

	// Deposit history.
	depRows, _ := h.db.Query(`
		SELECT investor_address, usdg_amount, basket_tokens_minted, timestamp, tx_hash
		FROM deposits WHERE basket_address = ? ORDER BY timestamp DESC LIMIT 50`, addr)
	defer depRows.Close()

	type DepositEntry struct {
		Investor           string `json:"investor"`
		UsdgAmount         string `json:"usdgAmount"`
		BasketTokensMinted string `json:"basketTokensMinted"`
		Timestamp          int64  `json:"timestamp"`
		TxHash             string `json:"txHash"`
	}
	var depHistory []DepositEntry
	for depRows.Next() {
		var e DepositEntry
		if depRows.Scan(&e.Investor, &e.UsdgAmount, &e.BasketTokensMinted, &e.Timestamp, &e.TxHash) == nil {
			depHistory = append(depHistory, e)
		}
	}
	if depHistory == nil {
		depHistory = []DepositEntry{}
	}

	// NAV change figures.
	var navPerToken, totalValueUsdg, navChange24h, navChange7d, navChange30d string
	var maxDriftBps int64
	var needsRebalancing bool

	if state != nil {
		navPerToken = state.NavPerToken
		totalValueUsdg = state.TotalValueUsdg
		maxDriftBps = state.MaxDriftBps
		needsRebalancing = state.NeedsRebalancing
	} else {
		// Fall back to latest nav_history entry.
		h.db.QueryRow(`
			SELECT nav_per_token, total_value_usdg FROM nav_history
			WHERE basket_address = ? ORDER BY timestamp DESC LIMIT 1`, addr,
		).Scan(&navPerToken, &totalValueUsdg)
	}

	navChange24h = h.navChangePct(addr, 86400)
	navChange7d = h.navChangePct(addr, 86400*7)
	navChange30d = h.navChangePct(addr, 86400*30)

	constituentsOut := []interface{}{}
	if state != nil {
		if v, ok := state.constituentsJSON().([]interface{}); ok && v != nil {
			constituentsOut = v
		}
	}

	jsonOK(w, map[string]any{
		"address":            b.Address,
		"creatorToken":       b.CreatorToken,
		"creator":            b.Creator,
		"name":               b.Name,
		"symbol":             b.Symbol,
		"thesis":             b.Thesis,
		"rebalancingEnabled": b.Rebalancing,
		"driftThresholdBps":  b.DriftThreshold,
		"createdAt":          b.CreatedAt,
		"suspended":          b.Suspended,
		"navPerToken":        navPerToken,
		"totalValueUsdg":     totalValueUsdg,
		"navChange24hPct":    navChange24h,
		"navChange7dPct":     navChange7d,
		"navChange30dPct":    navChange30d,
		"maxDriftBps":        maxDriftBps,
		"needsRebalancing":   needsRebalancing,
		"constituents":       constituentsOut,
		"performanceHistory": perf,
		"rebalanceHistory":   rebalHistory,
		"depositHistory":     depHistory,
	})
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

func (c *basketStateCache) constituentsJSON() any {
	if c == nil || c.ConstituentsJSON == "" {
		return []any{}
	}
	var v any
	if err := json.Unmarshal([]byte(c.ConstituentsJSON), &v); err != nil {
		return []any{}
	}
	return v
}

// getBasketStateFromCache reads from basket_state_cache if the entry is
// within 30 seconds. If stale or absent, reads live from the RPC node,
// writes the result back to the cache, and returns it.
// The cache is in SQLite — shared across all backend instances.
func (h *handler) getBasketStateFromCache(basketAddr string) (*basketStateCache, error) {
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

	// Cache miss or stale — fetch live and refresh.
	fresh, err := h.fetchBasketStateRPC(basketAddr)
	if err != nil {
		if c.BasketAddress != "" {
			// Return stale data rather than nothing if the RPC call fails.
			return &c, nil
		}
		return nil, err
	}

	h.writeBasketStateCache(fresh)
	return fresh, nil
}

func (h *handler) fetchBasketStateRPC(basketAddr string) (*basketStateCache, error) {
	basketStateABI, _ := abi.JSON(strings.NewReader(`[{
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

	rpcURL := os.Getenv("RPC_URL")
	if rpcURL == "" {
		rpcURL = "https://rpc.testnet.chain.robinhood.com"
	}

	client, err := ethclient.DialContext(context.Background(), rpcURL)
	if err != nil {
		return nil, fmt.Errorf("dial: %w", err)
	}
	defer client.Close()

	addr := common.HexToAddress(basketAddr)
	data, err := client.CallContract(context.Background(), ethereum.CallMsg{
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
	needsRebal, _ := unpacked[6].(bool)
	maxDrift, _ := unpacked[8].(*big.Int)

	// Collect all constituent addresses for a single supported_assets lookup.
	constituentAddrs := make([]string, len(constituents))
	for i, c := range constituents {
		constituentAddrs[i] = strings.ToLower(c.Hex())
	}

	// Fetch symbol and sector for all constituents in one query — no per-constituent queries.
	type assetMeta struct {
		symbol string
		sector string
	}
	metaByAddr := make(map[string]assetMeta, len(constituentAddrs))

	if len(constituentAddrs) > 0 {
		placeholders := make([]string, len(constituentAddrs))
		args := make([]interface{}, len(constituentAddrs))
		for i, a := range constituentAddrs {
			placeholders[i] = "?"
			args[i] = a
		}
		metaRows, err := h.db.Query(
			`SELECT address, symbol, sector FROM supported_assets WHERE address IN (`+
				strings.Join(placeholders, ",")+`)`,
			args...,
		)
		if err == nil {
			for metaRows.Next() {
				var addr, sym, sec string
				if metaRows.Scan(&addr, &sym, &sec) == nil {
					metaByAddr[addr] = assetMeta{symbol: sym, sector: sec}
				}
			}
			metaRows.Close()
		}
	}

	type constituentDetail struct {
		Address          string `json:"address"`
		Symbol           string `json:"symbol"`
		Sector           string `json:"sector"`
		TargetWeightBps  string `json:"targetWeightBps"`
		CurrentWeightBps string `json:"currentWeightBps"`
		BalanceRaw       string `json:"balanceRaw"`
	}

	details := make([]constituentDetail, 0, len(constituents))
	for i, c := range constituents {
		cAddr := strings.ToLower(c.Hex())
		meta := metaByAddr[cAddr]

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

		details = append(details, constituentDetail{
			Address:          cAddr,
			Symbol:           meta.symbol,
			Sector:           meta.sector,
			TargetWeightBps:  tw,
			CurrentWeightBps: cw,
			BalanceRaw:       bal,
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

// ── Catalogue ─────────────────────────────────────────────────────────────────

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

func (h *handler) getCatalogueAsset(w http.ResponseWriter, r *http.Request) {
	addr := strings.ToLower(r.PathValue("address"))

	var a struct {
		Address string `json:"address"`
		Symbol  string `json:"symbol"`
		Name    string `json:"name"`
		Sector  string `json:"sector"`
		Oracle  string `json:"oracle"`
		Active  bool   `json:"isActive"`
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

// ── Positions ─────────────────────────────────────────────────────────────────

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

	// Compute token balance: tokens minted minus tokens burned.
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

	// Current value = balance * navPerToken from nav_history.
	var navStr string
	h.db.QueryRow(`
		SELECT nav_per_token FROM nav_history
		WHERE basket_address = ? ORDER BY timestamp DESC LIMIT 1`, basketAddr,
	).Scan(&navStr)

	nav, _ := new(big.Float).SetString(navStr)
	if nav == nil {
		nav = new(big.Float)
	}

	currentValue := new(big.Float).Mul(balance, nav)
	// nav_per_token is 18-decimal; divide by 1e18 to get USDG 6-decimal value.
	currentValue.Quo(currentValue, new(big.Float).SetFloat64(1e18))

	costBasis := new(big.Float).Sub(dep, red)
	pnl := new(big.Float).Sub(currentValue, costBasis)

	pnlPct := "0.00"
	if costBasis.Sign() > 0 {
		p := new(big.Float).Quo(pnl, costBasis)
		p.Mul(p, new(big.Float).SetFloat64(100))
		pnlPct = p.Text('f', 2)
	}

	jsonOK(w, map[string]string{
		"basketAddress":      basketAddr,
		"walletAddress":      wallet,
		"basketTokenBalance": balance.Text('f', 0),
		"currentValueUsdg":   currentValue.Text('f', 0),
		"totalDepositedUsdg": dep.Text('f', 0),
		"unrealisedPnlUsdg":  pnl.Text('f', 0),
		"unrealisedPnlPct":   pnlPct,
	})
}

func (h *handler) getPortfolio(w http.ResponseWriter, r *http.Request) {
	wallet := strings.ToLower(r.PathValue("wallet"))

	// All deposit aggregates for this wallet grouped by basket — one query.
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

	agg := make(map[string]*basketAgg)
	var order []string

	depRows, err := h.db.Query(`
		SELECT d.basket_address,
		       b.name, b.symbol, b.rebalancing_enabled, b.suspended,
		       COALESCE(n.nav_per_token,'0'),
		       SUM(CAST(d.usdg_amount AS REAL))          AS total_dep,
		       SUM(CAST(d.basket_tokens_minted AS REAL))  AS total_minted
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
	depRows.Close()

	// All redemption aggregates for this wallet grouped by basket — one query.
	redRows, err := h.db.Query(`
		SELECT basket_address,
		       SUM(CAST(usdg_returned AS REAL))        AS total_red,
		       SUM(CAST(basket_tokens_burned AS REAL))  AS total_burned
		FROM redemptions
		WHERE investor_address = ?
		GROUP BY basket_address`, wallet)
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
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
	redRows.Close()

	type Position struct {
		BasketAddress      string `json:"basketAddress"`
		BasketName         string `json:"basketName"`
		BasketSymbol       string `json:"basketSymbol"`
		BasketNavPerToken  string `json:"basketNavPerToken"`
		RebalancingEnabled bool   `json:"rebalancingEnabled"`
		Suspended          bool   `json:"suspended"`
		BasketTokenBalance string `json:"basketTokenBalance"`
		CurrentValueUsdg   string `json:"currentValueUsdg"`
		TotalDepositedUsdg string `json:"totalDepositedUsdg"`
		UnrealisedPnlUsdg  string `json:"unrealisedPnlUsdg"`
		UnrealisedPnlPct   string `json:"unrealisedPnlPct"`
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

// ── Creator ───────────────────────────────────────────────────────────────────

func (h *handler) getCreatorDashboard(w http.ResponseWriter, r *http.Request) {
	wallet := strings.ToLower(r.PathValue("wallet"))

	// Fetch all baskets this wallet created with their latest NAV in one query.
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
	basketRows.Close()

	if len(basketOrder) == 0 {
		jsonOK(w, map[string]any{
			"walletAddress":      wallet,
			"totalClaimableUsdg": "0",
			"baskets":            []any{},
		})
		return
	}

	// Fetch ALL fee snapshots for ALL creator baskets in one query.
	// Build a placeholder list for the IN clause.
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

	// Group snapshots by basket address in Go — zero extra queries.
	snapshotsByBasket := make(map[string][]snapshotEntry)
	if err == nil {
		for snapRows.Next() {
			var bAddr string
			var s snapshotEntry
			if snapRows.Scan(&bAddr, &s.SnapshotID, &s.UsdgAmount, &s.Timestamp, &s.TxHash) == nil {
				snapshotsByBasket[bAddr] = append(snapshotsByBasket[bAddr], s)
			}
		}
		snapRows.Close()
	}

	type BasketEntry struct {
		BasketAddress      string          `json:"basketAddress"`
		BasketName         string          `json:"basketName"`
		BasketSymbol       string          `json:"basketSymbol"`
		CreatorToken       string          `json:"creatorTokenAddress"`
		TotalValueUsdg     string          `json:"totalValueUsdg"`
		TotalClaimableUsdg string          `json:"totalClaimableUsdg"`
		UnclaimedSnapshots []snapshotEntry `json:"unclaimedSnapshots"`
		RevenueHistory     []snapshotEntry `json:"revenueHistory"`
	}

	// Open one RPC client for all claimable revenue reads across all baskets.
	rpcURL := os.Getenv("RPC_URL")
	if rpcURL == "" {
		rpcURL = "https://rpc.testnet.chain.robinhood.com"
	}
	rpcClient, rpcErr := ethclient.DialContext(context.Background(), rpcURL)
	if rpcErr != nil {
		log.Printf("api: getCreatorDashboard dial: %v", rpcErr)
		rpcClient = nil
	}
	if rpcClient != nil {
		defer rpcClient.Close()
	}

	totalClaimable := new(big.Int)
	result := make([]BasketEntry, 0, len(basketOrder))

	for _, addr := range basketOrder {
		m := baskets[addr]
		snaps := snapshotsByBasket[addr]
		if snaps == nil {
			snaps = []snapshotEntry{}
		}

		unclaimed := h.getClaimableSnapshots(rpcClient, wallet, addr, m.creatorToken, snaps)

		basketClaimable := new(big.Int)
		for _, s := range unclaimed {
			if amt, ok := new(big.Int).SetString(s.ClaimableUsdg, 10); ok {
				basketClaimable.Add(basketClaimable, amt)
			}
		}
		totalClaimable.Add(totalClaimable, basketClaimable)

		result = append(result, BasketEntry{
			BasketAddress:      addr,
			BasketName:         m.name,
			BasketSymbol:       m.symbol,
			CreatorToken:       m.creatorToken,
			TotalValueUsdg:     m.totalValue,
			TotalClaimableUsdg: basketClaimable.String(),
			UnclaimedSnapshots: unclaimed,
			RevenueHistory:     snaps,
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
// on the contract and writing results back to the cache.
func (h *handler) getClaimableSnapshots(
	client *ethclient.Client,
	wallet, basketAddr, creatorTokenAddr string,
	snapshots []snapshotEntry,
) []snapshotEntry {
	if len(snapshots) == 0 {
		return []snapshotEntry{}
	}

	claimableABI, _ := abi.JSON(strings.NewReader(`[{
		"inputs":[
			{"internalType":"address","name":"account",    "type":"address"},
			{"internalType":"uint256","name":"snapshotId", "type":"uint256"}
		],
		"name":"claimableRevenue",
		"outputs":[{"internalType":"uint256","name":"","type":"uint256"}],
		"stateMutability":"view",
		"type":"function"
	}]`))

	now := time.Now().Unix()

	// Fetch all cached entries for this wallet+basket in one query.
	// Map key is snapshot_id for O(1) lookup inside the loop.
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
		for cacheRows.Next() {
			var snapID int64
			var ce cachedEntry
			if cacheRows.Scan(&snapID, &ce.claimableUsdg, &ce.cachedAt) == nil {
				cache[snapID] = ce
			}
		}
		cacheRows.Close()
	}

	ctAddr := common.HexToAddress(creatorTokenAddr)
	walletAddr := common.HexToAddress(wallet)
	result := make([]snapshotEntry, 0, len(snapshots))

	for _, snap := range snapshots {
		// Check in-memory cache map — zero additional queries per snapshot.
		if ce, ok := cache[snap.SnapshotID]; ok && now-ce.cachedAt <= 60 {
			snap.ClaimableUsdg = ce.claimableUsdg
			result = append(result, snap)
			continue
		}

		// Cache miss or stale — call contract if client is available.
		claimable := "0"
		if client != nil {
			input, err := claimableABI.Pack("claimableRevenue",
				walletAddr,
				new(big.Int).SetInt64(snap.SnapshotID),
			)
			if err == nil {
				data, err := client.CallContract(context.Background(), ethereum.CallMsg{
					To:   &ctAddr,
					Data: input,
				}, nil)
				if err == nil {
					unpacked, err := claimableABI.Methods["claimableRevenue"].Outputs.Unpack(data)
					if err == nil && len(unpacked) > 0 {
						if amount, ok := unpacked[0].(*big.Int); ok && amount != nil {
							claimable = amount.String()
						}
					}
				}
			}

			// Write back to cache — one upsert per snapshot that was a cache miss.
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

// ── AI Compose ────────────────────────────────────────────────────────────────

func (h *handler) aiCompose(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Thesis string `json:"thesis"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil || len(req.Thesis) < 20 {
		jsonError(w, "thesis must be at least 20 characters", http.StatusBadRequest)
		return
	}

	// Fetch catalogue from the database to pass to the AI composer.
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

// ── Helpers ───────────────────────────────────────────────────────────────────

func jsonOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(v)
}

func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]any{
		"error": msg,
		"code":  code,
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
