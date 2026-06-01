package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"log"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/OCD-Labs/Weave/server/db"
)

// NewRouter wires all HTTP routes and returns the handler.
func NewRouter(database *db.DB, aiBaseURL string, agentAPIKey string) http.Handler {
	mux := http.NewServeMux()

	h := &handler{db: database, aiBaseURL: aiBaseURL, agentAPIKey: agentAPIKey}

	mux.HandleFunc("GET /baskets",                       h.listBaskets)
	mux.HandleFunc("GET /baskets/{address}",             h.getBasket)
	mux.HandleFunc("GET /baskets/{address}/performance", h.getBasketPerformance)
	mux.HandleFunc("GET /baskets/{address}/positions/{wallet}", h.getPosition)
	mux.HandleFunc("GET /catalogue",                    h.getCatalogue)
	mux.HandleFunc("GET /catalogue/{address}",          h.getCatalogueAsset)
	mux.HandleFunc("GET /prices",                       h.getPrices)
	mux.HandleFunc("GET /positions/{wallet}",           h.getPortfolio)
	mux.HandleFunc("GET /creator/{wallet}",             h.getCreatorDashboard)
	mux.HandleFunc("GET /creator-tokens/{address}",     h.getCreatorToken)
	mux.HandleFunc("POST /ai/compose",                  h.aiCompose)

	mux.HandleFunc("GET /docs", h.serveDocs)
	mux.HandleFunc("GET /openapi.json", h.serveOpenAPI)

	return corsMiddleware(mux)
}

type handler struct {
	db          *db.DB
	aiBaseURL   string
	agentAPIKey string
}

func (h *handler) listBaskets(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.Query(`
		SELECT b.address, b.creator_token_address, b.creator_address, b.name, b.symbol,
		       b.thesis, b.rebalancing_enabled, b.drift_threshold_bps,
		       b.created_at, b.suspended,
		       COALESCE(n.nav_per_token, '0')      AS nav_per_token,
		       COALESCE(n.total_value_usdg, '0')   AS total_value_usdg
		FROM baskets b
		LEFT JOIN (
			SELECT basket_address, nav_per_token, total_value_usdg,
			       MAX(timestamp) AS ts
			FROM nav_history GROUP BY basket_address
		) n ON n.basket_address = b.address
		ORDER BY b.created_at DESC`,
	)
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type BasketRow struct {
		Address         string `json:"address"`
		CreatorToken    string `json:"creatorToken"`
		Creator         string `json:"creator"`
		Name            string `json:"name"`
		Symbol          string `json:"symbol"`
		Thesis          string `json:"thesis"`
		Rebalancing     bool   `json:"rebalancingEnabled"`
		DriftThreshold  *int64 `json:"driftThresholdBps"`
		CreatedAt       int64  `json:"createdAt"`
		Suspended       bool   `json:"suspended"`
		NavPerToken     string `json:"navPerToken"`
		TotalValueUsdg  string `json:"totalValueUsdg"`
	}

	var baskets []BasketRow
	for rows.Next() {
		var b BasketRow
		var rebal, susp int
		var drift sql.NullInt64

		if err := rows.Scan(
			&b.Address, &b.CreatorToken, &b.Creator, &b.Name, &b.Symbol,
			&b.Thesis, &rebal, &drift, &b.CreatedAt, &susp,
			&b.NavPerToken, &b.TotalValueUsdg,
		); err != nil {
			continue
		}

		b.Rebalancing = rebal == 1
		b.Suspended   = susp == 1
		if drift.Valid {
			b.DriftThreshold = &drift.Int64
		}

		baskets = append(baskets, b)
	}

	if baskets == nil {
		baskets = []BasketRow{}
	}

	jsonOK(w, baskets)
}

func (h *handler) getBasket(w http.ResponseWriter, r *http.Request) {
	addr := strings.ToLower(r.PathValue("address"))

	var basket struct {
		Address        string  `json:"address"`
		CreatorToken   string  `json:"creatorToken"`
		Creator        string  `json:"creator"`
		Name           string  `json:"name"`
		Symbol         string  `json:"symbol"`
		Thesis         string  `json:"thesis"`
		Rebalancing    bool    `json:"rebalancingEnabled"`
		DriftThreshold *int64  `json:"driftThresholdBps"`
		CreatedAt      int64   `json:"createdAt"`
		Suspended      bool    `json:"suspended"`
	}

	var rebal, susp int
	var drift sql.NullInt64

	err := h.db.QueryRow(`
		SELECT address, creator_token_address, creator_address, name, symbol, thesis,
		       rebalancing_enabled, drift_threshold_bps, created_at, suspended
		FROM baskets WHERE address = ?`, addr).Scan(
		&basket.Address, &basket.CreatorToken, &basket.Creator,
		&basket.Name, &basket.Symbol, &basket.Thesis,
		&rebal, &drift, &basket.CreatedAt, &susp,
	)
	if err == sql.ErrNoRows {
		jsonError(w, "basket not found", http.StatusNotFound)
		return
	}
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}

	basket.Rebalancing = rebal == 1
	basket.Suspended   = susp == 1
	if drift.Valid {
		basket.DriftThreshold = &drift.Int64
	}

	jsonOK(w, basket)
}

func (h *handler) getBasketPerformance(w http.ResponseWriter, r *http.Request) {
	addr := strings.ToLower(r.PathValue("address"))

	rows, err := h.db.Query(`
		SELECT nav_per_token, total_value_usdg, timestamp
		FROM nav_history
		WHERE basket_address = ?
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
	wallet     := strings.ToLower(r.PathValue("wallet"))

	// Sum all deposits and redemptions for this wallet in this basket.
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

	dep, _  := new(big.Float).SetString(totalDeposited)
	red, _  := new(big.Float).SetString(totalRedeemed)
	if dep == nil { dep = new(big.Float) }
	if red == nil { red = new(big.Float) }

	costBasis := new(big.Float).Sub(dep, red)

	jsonOK(w, map[string]string{
		"basketAddress":    basketAddr,
		"walletAddress":    wallet,
		"totalDepositedUsdg": dep.Text('f', 0),
		"totalRedeemedUsdg":  red.Text('f', 0),
		"netCostBasisUsdg":   costBasis.Text('f', 0),
	})
}

func (h *handler) getCatalogue(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.Query(`
		SELECT a.address, a.symbol, a.name, a.sector, a.oracle_address, a.is_active,
		       COALESCE(p.price_usdg, '0') AS price,
		       p.timestamp
		FROM supported_assets a
		LEFT JOIN (
			SELECT stock_address, price_usdg, MAX(timestamp) AS timestamp
			FROM price_history GROUP BY stock_address
		) p ON p.stock_address = a.address
		ORDER BY a.symbol ASC`)
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type Asset struct {
		Address      string `json:"address"`
		Symbol       string `json:"symbol"`
		Name         string `json:"name"`
		Sector       string `json:"sector"`
		Oracle       string `json:"oracle"`
		Active       bool   `json:"isActive"`
		CurrentPrice string `json:"currentPriceUsdg"`
		PriceAt      *int64 `json:"priceUpdatedAt"`
	}

	var assets []Asset
	for rows.Next() {
		var a Asset
		var active int
		var priceAt sql.NullInt64

		if err := rows.Scan(&a.Address, &a.Symbol, &a.Name, &a.Sector,
			&a.Oracle, &active, &a.CurrentPrice, &priceAt); err != nil {
			continue
		}

		a.Active = active == 1
		if priceAt.Valid {
			a.PriceAt = &priceAt.Int64
		}

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
		SELECT p.stock_address, p.price_usdg, p.timestamp
		FROM price_history p
		INNER JOIN (
			SELECT stock_address, MAX(timestamp) AS ts
			FROM price_history GROUP BY stock_address
		) latest ON latest.stock_address = p.stock_address AND latest.ts = p.timestamp`)
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type Price struct {
		Address   string `json:"address"`
		Price     string `json:"priceUsdg"`
		UpdatedAt int64  `json:"updatedAt"`
	}

	var prices []Price
	for rows.Next() {
		var p Price
		if err := rows.Scan(&p.Address, &p.Price, &p.UpdatedAt); err != nil {
			continue
		}
		prices = append(prices, p)
	}

	if prices == nil {
		prices = []Price{}
	}

	jsonOK(w, prices)
}

func (h *handler) getPortfolio(w http.ResponseWriter, r *http.Request) {
	wallet := strings.ToLower(r.PathValue("wallet"))

	// Find all baskets this wallet has deposited into.
	rows, err := h.db.Query(`
		SELECT DISTINCT basket_address FROM deposits WHERE investor_address = ?`, wallet)
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type Position struct {
		BasketAddress     string `json:"basketAddress"`
		TotalDepositedUsdg string `json:"totalDepositedUsdg"`
		TotalRedeemedUsdg  string `json:"totalRedeemedUsdg"`
		NetCostBasisUsdg   string `json:"netCostBasisUsdg"`
	}

	var positions []Position
	for rows.Next() {
		var basketAddr string
		if err := rows.Scan(&basketAddr); err != nil {
			continue
		}

		var dep, red string
		h.db.QueryRow(`SELECT COALESCE(SUM(CAST(usdg_amount AS REAL)), 0) FROM deposits WHERE basket_address = ? AND investor_address = ?`, basketAddr, wallet).Scan(&dep)
		h.db.QueryRow(`SELECT COALESCE(SUM(CAST(usdg_returned AS REAL)), 0) FROM redemptions WHERE basket_address = ? AND investor_address = ?`, basketAddr, wallet).Scan(&red)

		depF, _ := new(big.Float).SetString(dep)
		redF, _ := new(big.Float).SetString(red)
		if depF == nil { depF = new(big.Float) }
		if redF == nil { redF = new(big.Float) }

		net := new(big.Float).Sub(depF, redF)

		positions = append(positions, Position{
			BasketAddress:      basketAddr,
			TotalDepositedUsdg: depF.Text('f', 0),
			TotalRedeemedUsdg:  redF.Text('f', 0),
			NetCostBasisUsdg:   net.Text('f', 0),
		})
	}

	if positions == nil {
		positions = []Position{}
	}

	jsonOK(w, map[string]interface{}{
		"walletAddress": wallet,
		"positions":     positions,
	})
}

func (h *handler) getCreatorDashboard(w http.ResponseWriter, r *http.Request) {
	wallet := strings.ToLower(r.PathValue("wallet"))

	rows, err := h.db.Query(`
		SELECT address, creator_token_address, name, symbol
		FROM baskets WHERE creator_address = ?
		ORDER BY created_at DESC`, wallet)
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type BasketEntry struct {
		BasketAddress string `json:"basketAddress"`
		CreatorToken  string `json:"creatorTokenAddress"`
		Name          string `json:"name"`
		Symbol        string `json:"symbol"`
	}

	var entries []BasketEntry
	for rows.Next() {
		var e BasketEntry
		if err := rows.Scan(&e.BasketAddress, &e.CreatorToken, &e.Name, &e.Symbol); err != nil {
			continue
		}
		entries = append(entries, e)
	}

	if entries == nil {
		entries = []BasketEntry{}
	}

	jsonOK(w, map[string]interface{}{
		"walletAddress": wallet,
		"baskets":       entries,
	})
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

	jsonOK(w, map[string]interface{}{
		"creatorTokenAddress": addr,
		"totalRevenueUsdg":    totalRevenue.String(),
		"snapshots":           snapshots,
	})
}

func (h *handler) aiCompose(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 4096))
	if err != nil {
		jsonError(w, "bad request", http.StatusBadRequest)
		return
	}

	aiURL := h.aiBaseURL + "/compose"
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, aiURL, bytes.NewReader(body))
	if err != nil {
		jsonError(w, "ai service error", http.StatusServiceUnavailable)
		return
	}

	req.Header.Set("Content-Type", "application/json")

	// Forward API key to agent service if configured
	if h.agentAPIKey != "" {
		req.Header.Set("x-api-key", h.agentAPIKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("api: ai service error: %v", err)
		jsonError(w, "ai service unavailable", http.StatusServiceUnavailable)
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

func jsonOK(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(v)
}

func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]interface{}{
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

		// Log every request with method, path, and latency.
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start))
	})
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