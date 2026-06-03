package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ── catalogue helpers ─────────────────────────────────────────────────────────

func makeCatalogue(n int) []CatalogueAsset {
	assets := make([]CatalogueAsset, n)
	syms := []string{"TSLA", "AMZN", "PLTR", "NFLX", "AMD", "NVDA", "MSFT", "AAPL", "GOOG", "META", "AMGN", "PFE"}
	addrs := []string{
		"0x1000000000000000000000000000000000000001",
		"0x1000000000000000000000000000000000000002",
		"0x1000000000000000000000000000000000000003",
		"0x1000000000000000000000000000000000000004",
		"0x1000000000000000000000000000000000000005",
		"0x1000000000000000000000000000000000000006",
		"0x1000000000000000000000000000000000000007",
		"0x1000000000000000000000000000000000000008",
		"0x1000000000000000000000000000000000000009",
		"0x100000000000000000000000000000000000000a",
		"0x100000000000000000000000000000000000000b",
		"0x100000000000000000000000000000000000000c",
	}
	for i := 0; i < n; i++ {
		assets[i] = CatalogueAsset{
			Address:          addrs[i%len(addrs)],
			Symbol:           syms[i%len(syms)],
			Name:             syms[i%len(syms)] + " Inc",
			Sector:           "Technology",
			CurrentPriceUsdg: "10000000000",
		}
	}
	return assets
}

func validProposalJSON(catalogue []CatalogueAsset) string {
	type c struct {
		Address   string `json:"address"`
		Symbol    string `json:"symbol"`
		WeightBps int    `json:"weightBps"`
		Rationale string `json:"rationale"`
	}
	constituents := []c{
		{Address: catalogue[0].Address, Symbol: catalogue[0].Symbol, WeightBps: 4000, Rationale: "primary play"},
		{Address: catalogue[1].Address, Symbol: catalogue[1].Symbol, WeightBps: 3000, Rationale: "secondary play"},
		{Address: catalogue[2].Address, Symbol: catalogue[2].Symbol, WeightBps: 3000, Rationale: "tertiary play"},
	}
	b, _ := json.Marshal(map[string]interface{}{
		"constituents":     constituents,
		"overallRationale": "a solid thesis",
		"riskNotes":        "concentration risk",
	})
	return string(b)
}

// ── stripAndValidate ──────────────────────────────────────────────────────────

func TestStripAndValidate_ValidProposal(t *testing.T) {
	cat := makeCatalogue(5)
	raw := validProposalJSON(cat)

	proposal, err := stripAndValidate(raw, cat)
	if err != nil {
		t.Fatalf("expected valid proposal, got error: %v", err)
	}
	if len(proposal.Constituents) != 3 {
		t.Errorf("expected 3 constituents, got %d", len(proposal.Constituents))
	}
	if proposal.OverallRationale == "" {
		t.Error("expected non-empty overallRationale")
	}
	if proposal.RiskNotes == "" {
		t.Error("expected non-empty riskNotes")
	}
}

func TestStripAndValidate_StripsMarkdownFences(t *testing.T) {
	cat := makeCatalogue(5)
	raw := "```json\n" + validProposalJSON(cat) + "\n```"

	_, err := stripAndValidate(raw, cat)
	if err != nil {
		t.Fatalf("expected fence stripping to succeed, got: %v", err)
	}
}

func TestStripAndValidate_StripsPlainFences(t *testing.T) {
	cat := makeCatalogue(5)
	raw := "```\n" + validProposalJSON(cat) + "\n```"

	_, err := stripAndValidate(raw, cat)
	if err != nil {
		t.Fatalf("expected plain fence stripping to succeed, got: %v", err)
	}
}

func TestStripAndValidate_TooFewConstituents(t *testing.T) {
	cat := makeCatalogue(5)
	raw, _ := json.Marshal(map[string]interface{}{
		"constituents": []map[string]interface{}{
			{"address": cat[0].Address, "symbol": cat[0].Symbol, "weightBps": 5000, "rationale": "only one"},
			{"address": cat[1].Address, "symbol": cat[1].Symbol, "weightBps": 5000, "rationale": "only two"},
		},
		"overallRationale": "too few",
		"riskNotes":        "risky",
	})

	_, err := stripAndValidate(string(raw), cat)
	if err == nil {
		t.Fatal("expected error for too few constituents")
	}
	if !strings.Contains(err.Error(), "too few") {
		t.Errorf("expected 'too few' in error, got: %v", err)
	}
}

func TestStripAndValidate_TooManyConstituents(t *testing.T) {
	cat := makeCatalogue(12)
	type c struct {
		Address   string `json:"address"`
		Symbol    string `json:"symbol"`
		WeightBps int    `json:"weightBps"`
		Rationale string `json:"rationale"`
	}
	constituents := make([]c, 13)
	// Can't have 13 unique from our 12-address catalogue so reuse — validation
	// catches too-many before duplicate check.
	for i := range constituents {
		idx := i % 12
		constituents[i] = c{
			Address:   cat[idx].Address,
			Symbol:    cat[idx].Symbol,
			WeightBps: 100,
			Rationale: "filler",
		}
	}
	raw, _ := json.Marshal(map[string]interface{}{
		"constituents":     constituents,
		"overallRationale": "too many",
		"riskNotes":        "risky",
	})

	_, err := stripAndValidate(string(raw), cat)
	if err == nil {
		t.Fatal("expected error for too many constituents")
	}
	if !strings.Contains(err.Error(), "too many") {
		t.Errorf("expected 'too many' in error, got: %v", err)
	}
}

func TestStripAndValidate_WeightSumNot10000(t *testing.T) {
	cat := makeCatalogue(5)
	raw, _ := json.Marshal(map[string]interface{}{
		"constituents": []map[string]interface{}{
			{"address": cat[0].Address, "symbol": cat[0].Symbol, "weightBps": 4000, "rationale": "a"},
			{"address": cat[1].Address, "symbol": cat[1].Symbol, "weightBps": 3000, "rationale": "b"},
			{"address": cat[2].Address, "symbol": cat[2].Symbol, "weightBps": 2999, "rationale": "c"},
		},
		"overallRationale": "bad weights",
		"riskNotes":        "risky",
	})

	_, err := stripAndValidate(string(raw), cat)
	if err == nil {
		t.Fatal("expected error for wrong weight sum")
	}
	if !strings.Contains(err.Error(), "9999") {
		t.Errorf("expected weight sum in error, got: %v", err)
	}
}

func TestStripAndValidate_WeightTooLow(t *testing.T) {
	cat := makeCatalogue(5)
	raw, _ := json.Marshal(map[string]interface{}{
		"constituents": []map[string]interface{}{
			{"address": cat[0].Address, "symbol": cat[0].Symbol, "weightBps": 99, "rationale": "a"},
			{"address": cat[1].Address, "symbol": cat[1].Symbol, "weightBps": 5000, "rationale": "b"},
			{"address": cat[2].Address, "symbol": cat[2].Symbol, "weightBps": 4901, "rationale": "c"},
		},
		"overallRationale": "low weight",
		"riskNotes":        "risky",
	})

	_, err := stripAndValidate(string(raw), cat)
	if err == nil {
		t.Fatal("expected error for weight below 100")
	}
	if !strings.Contains(err.Error(), "too low") {
		t.Errorf("expected 'too low' in error, got: %v", err)
	}
}

func TestStripAndValidate_WeightTooHigh(t *testing.T) {
	cat := makeCatalogue(5)
	raw, _ := json.Marshal(map[string]interface{}{
		"constituents": []map[string]interface{}{
			{"address": cat[0].Address, "symbol": cat[0].Symbol, "weightBps": 5001, "rationale": "a"},
			{"address": cat[1].Address, "symbol": cat[1].Symbol, "weightBps": 2500, "rationale": "b"},
			{"address": cat[2].Address, "symbol": cat[2].Symbol, "weightBps": 2499, "rationale": "c"},
		},
		"overallRationale": "high weight",
		"riskNotes":        "risky",
	})

	_, err := stripAndValidate(string(raw), cat)
	if err == nil {
		t.Fatal("expected error for weight above 5000")
	}
	if !strings.Contains(err.Error(), "too high") {
		t.Errorf("expected 'too high' in error, got: %v", err)
	}
}

func TestStripAndValidate_AddressNotInCatalogue(t *testing.T) {
	cat := makeCatalogue(5)
	raw, _ := json.Marshal(map[string]interface{}{
		"constituents": []map[string]interface{}{
			{"address": "0xdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef", "symbol": "FAKE", "weightBps": 4000, "rationale": "a"},
			{"address": cat[1].Address, "symbol": cat[1].Symbol, "weightBps": 3000, "rationale": "b"},
			{"address": cat[2].Address, "symbol": cat[2].Symbol, "weightBps": 3000, "rationale": "c"},
		},
		"overallRationale": "bad address",
		"riskNotes":        "risky",
	})

	_, err := stripAndValidate(string(raw), cat)
	if err == nil {
		t.Fatal("expected error for address not in catalogue")
	}
	if !strings.Contains(err.Error(), "not in catalogue") {
		t.Errorf("expected 'not in catalogue' in error, got: %v", err)
	}
}

func TestStripAndValidate_SymbolMismatch(t *testing.T) {
	cat := makeCatalogue(5)
	raw, _ := json.Marshal(map[string]interface{}{
		"constituents": []map[string]interface{}{
			{"address": cat[0].Address, "symbol": "WRONGSYM", "weightBps": 4000, "rationale": "a"},
			{"address": cat[1].Address, "symbol": cat[1].Symbol, "weightBps": 3000, "rationale": "b"},
			{"address": cat[2].Address, "symbol": cat[2].Symbol, "weightBps": 3000, "rationale": "c"},
		},
		"overallRationale": "wrong symbol",
		"riskNotes":        "risky",
	})

	_, err := stripAndValidate(string(raw), cat)
	if err == nil {
		t.Fatal("expected error for symbol mismatch")
	}
	if !strings.Contains(err.Error(), "mismatch") {
		t.Errorf("expected 'mismatch' in error, got: %v", err)
	}
}

func TestStripAndValidate_DuplicateAddress(t *testing.T) {
	cat := makeCatalogue(5)
	raw, _ := json.Marshal(map[string]interface{}{
		"constituents": []map[string]interface{}{
			{"address": cat[0].Address, "symbol": cat[0].Symbol, "weightBps": 4000, "rationale": "a"},
			{"address": cat[0].Address, "symbol": cat[0].Symbol, "weightBps": 3000, "rationale": "b"},
			{"address": cat[1].Address, "symbol": cat[1].Symbol, "weightBps": 3000, "rationale": "c"},
		},
		"overallRationale": "duplicate",
		"riskNotes":        "risky",
	})

	_, err := stripAndValidate(string(raw), cat)
	if err == nil {
		t.Fatal("expected error for duplicate address")
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("expected 'duplicate' in error, got: %v", err)
	}
}

func TestStripAndValidate_EmptyRationale(t *testing.T) {
	cat := makeCatalogue(5)
	raw, _ := json.Marshal(map[string]interface{}{
		"constituents": []map[string]interface{}{
			{"address": cat[0].Address, "symbol": cat[0].Symbol, "weightBps": 4000, "rationale": ""},
			{"address": cat[1].Address, "symbol": cat[1].Symbol, "weightBps": 3000, "rationale": "b"},
			{"address": cat[2].Address, "symbol": cat[2].Symbol, "weightBps": 3000, "rationale": "c"},
		},
		"overallRationale": "empty rat",
		"riskNotes":        "risky",
	})

	_, err := stripAndValidate(string(raw), cat)
	if err == nil {
		t.Fatal("expected error for empty rationale")
	}
	if !strings.Contains(err.Error(), "rationale") {
		t.Errorf("expected 'rationale' in error, got: %v", err)
	}
}

func TestStripAndValidate_EmptyOverallRationale(t *testing.T) {
	cat := makeCatalogue(5)
	raw, _ := json.Marshal(map[string]interface{}{
		"constituents": []map[string]interface{}{
			{"address": cat[0].Address, "symbol": cat[0].Symbol, "weightBps": 4000, "rationale": "a"},
			{"address": cat[1].Address, "symbol": cat[1].Symbol, "weightBps": 3000, "rationale": "b"},
			{"address": cat[2].Address, "symbol": cat[2].Symbol, "weightBps": 3000, "rationale": "c"},
		},
		"overallRationale": "",
		"riskNotes":        "risky",
	})

	_, err := stripAndValidate(string(raw), cat)
	if err == nil {
		t.Fatal("expected error for empty overallRationale")
	}
}

func TestStripAndValidate_EmptyRiskNotes(t *testing.T) {
	cat := makeCatalogue(5)
	raw, _ := json.Marshal(map[string]interface{}{
		"constituents": []map[string]interface{}{
			{"address": cat[0].Address, "symbol": cat[0].Symbol, "weightBps": 4000, "rationale": "a"},
			{"address": cat[1].Address, "symbol": cat[1].Symbol, "weightBps": 3000, "rationale": "b"},
			{"address": cat[2].Address, "symbol": cat[2].Symbol, "weightBps": 3000, "rationale": "c"},
		},
		"overallRationale": "fine",
		"riskNotes":        "",
	})

	_, err := stripAndValidate(string(raw), cat)
	if err == nil {
		t.Fatal("expected error for empty riskNotes")
	}
}

func TestStripAndValidate_InvalidJSON(t *testing.T) {
	cat := makeCatalogue(5)
	_, err := stripAndValidate("this is not json", cat)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestStripAndValidate_AddressCaseInsensitive(t *testing.T) {
	cat := makeCatalogue(5)
	// Submit address in uppercase — validation must normalise before lookup.
	upper := strings.ToUpper(cat[0].Address)
	raw, _ := json.Marshal(map[string]interface{}{
		"constituents": []map[string]interface{}{
			{"address": upper, "symbol": cat[0].Symbol, "weightBps": 4000, "rationale": "a"},
			{"address": cat[1].Address, "symbol": cat[1].Symbol, "weightBps": 3000, "rationale": "b"},
			{"address": cat[2].Address, "symbol": cat[2].Symbol, "weightBps": 3000, "rationale": "c"},
		},
		"overallRationale": "case test",
		"riskNotes":        "risky",
	})

	_, err := stripAndValidate(string(raw), cat)
	if err != nil {
		t.Fatalf("expected case-insensitive address match, got: %v", err)
	}
}

func TestStripAndValidate_ExactlyThreeConstituents(t *testing.T) {
	cat := makeCatalogue(5)
	_, err := stripAndValidate(validProposalJSON(cat), cat)
	if err != nil {
		t.Fatalf("exactly 3 constituents should pass: %v", err)
	}
}

func TestStripAndValidate_ExactlyTwelveConstituents(t *testing.T) {
	cat := makeCatalogue(12)
	type c struct {
		Address   string `json:"address"`
		Symbol    string `json:"symbol"`
		WeightBps int    `json:"weightBps"`
		Rationale string `json:"rationale"`
	}
	constituents := make([]c, 12)
	weightEach := 833
	for i := range constituents {
		constituents[i] = c{
			Address:   cat[i].Address,
			Symbol:    cat[i].Symbol,
			WeightBps: weightEach,
			Rationale: "constituent " + cat[i].Symbol,
		}
	}
	// Adjust last to make sum exactly 10000.
	constituents[11].WeightBps = 10000 - weightEach*11

	raw, _ := json.Marshal(map[string]interface{}{
		"constituents":     constituents,
		"overallRationale": "twelve stocks",
		"riskNotes":        "diversified",
	})

	_, err := stripAndValidate(string(raw), cat)
	if err != nil {
		t.Fatalf("exactly 12 constituents should pass: %v", err)
	}
}

// ── systemPrompt / userPrompt / retryNote ─────────────────────────────────────

func TestSystemPrompt_ContainsKeyRules(t *testing.T) {
	sp := systemPrompt()

	required := []string{
		"10,000",
		"100",
		"5000",
		"3",
		"12",
		"JSON",
		"constituents",
		"weightBps",
		"overallRationale",
		"riskNotes",
	}

	for _, r := range required {
		if !strings.Contains(sp, r) {
			t.Errorf("systemPrompt missing required string: %q", r)
		}
	}
}

func TestSystemPrompt_NotEmpty(t *testing.T) {
	if len(systemPrompt()) == 0 {
		t.Error("systemPrompt must not be empty")
	}
}

func TestUserPrompt_ContainsThesis(t *testing.T) {
	cat := makeCatalogue(3)
	thesis := "companies building AI data centres"
	up := userPrompt(thesis, cat)

	if !strings.Contains(up, thesis) {
		t.Error("userPrompt must contain the thesis")
	}
}

func TestUserPrompt_ContainsCatalogueSymbols(t *testing.T) {
	cat := makeCatalogue(3)
	up := userPrompt("a thesis", cat)

	for _, a := range cat {
		if !strings.Contains(up, a.Symbol) {
			t.Errorf("userPrompt missing catalogue symbol %s", a.Symbol)
		}
	}
}

func TestUserPrompt_ContainsCatalogueAddresses(t *testing.T) {
	cat := makeCatalogue(3)
	up := userPrompt("a thesis", cat)

	for _, a := range cat {
		if !strings.Contains(up, a.Address) {
			t.Errorf("userPrompt missing catalogue address %s", a.Address)
		}
	}
}

func TestRetryNote_NotEmpty(t *testing.T) {
	if len(retryNote()) == 0 {
		t.Error("retryNote must not be empty")
	}
}

func TestRetryNote_ContainsJSONInstruction(t *testing.T) {
	rn := retryNote()
	if !strings.Contains(rn, "JSON") {
		t.Error("retryNote must mention JSON")
	}
}

// ── Composer HTTP call ────────────────────────────────────────────────────────

func TestComposer_SuccessfulCall(t *testing.T) {
	cat := makeCatalogue(5)
	responseBody := validProposalJSON(cat)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			t.Error("expected Authorization header")
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Error("expected Content-Type application/json")
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{
				{"message": map[string]string{"content": responseBody}},
			},
		})
	}))
	defer srv.Close()

	// Patch the endpoint for testing by calling call() directly.
	// We test the full Compose() path via the HTTP server.
	c := &Composer{apiKey: "test-key", model: "gpt-4.1-mini"}

	// Use a custom http.Client that redirects to the test server.
	origClient := http.DefaultClient
	http.DefaultClient = &http.Client{
		Transport: &testTransport{base: srv.URL},
	}
	defer func() { http.DefaultClient = origClient }()

	proposal, err := c.Compose(context.Background(), "AI infrastructure companies", cat)
	if err != nil {
		t.Fatalf("expected successful compose, got: %v", err)
	}
	if len(proposal.Constituents) != 3 {
		t.Errorf("expected 3 constituents, got %d", len(proposal.Constituents))
	}
	if !strings.HasPrefix(proposal.Provider, "openai/") {
		t.Errorf("expected provider to start with 'openai/', got %s", proposal.Provider)
	}
}

func TestComposer_RetriesOnInvalidJSON(t *testing.T) {
	cat := makeCatalogue(5)
	calls := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			// First call returns invalid JSON.
			json.NewEncoder(w).Encode(map[string]interface{}{
				"choices": []map[string]interface{}{
					{"message": map[string]string{"content": "not valid json at all"}},
				},
			})
		} else {
			// Second call returns valid proposal.
			json.NewEncoder(w).Encode(map[string]interface{}{
				"choices": []map[string]interface{}{
					{"message": map[string]string{"content": validProposalJSON(cat)}},
				},
			})
		}
	}))
	defer srv.Close()

	c := &Composer{apiKey: "test-key", model: "gpt-4.1-mini"}

	origClient := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: &testTransport{base: srv.URL}}
	defer func() { http.DefaultClient = origClient }()

	proposal, err := c.Compose(context.Background(), "a thesis about tech", cat)
	if err != nil {
		t.Fatalf("expected retry to succeed, got: %v", err)
	}
	if calls != 2 {
		t.Errorf("expected exactly 2 calls (initial + retry), got %d", calls)
	}
	if proposal == nil {
		t.Error("expected non-nil proposal after retry")
	}
}

func TestComposer_FailsAfterRetry(t *testing.T) {
	cat := makeCatalogue(5)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{
				{"message": map[string]string{"content": "still not json"}},
			},
		})
	}))
	defer srv.Close()

	c := &Composer{apiKey: "test-key", model: "gpt-4.1-mini"}

	origClient := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: &testTransport{base: srv.URL}}
	defer func() { http.DefaultClient = origClient }()

	_, err := c.Compose(context.Background(), "a thesis", cat)
	if err == nil {
		t.Fatal("expected error after two failed attempts")
	}
	if !strings.Contains(err.Error(), "validation failed after retry") {
		t.Errorf("expected 'validation failed after retry' in error, got: %v", err)
	}
}

func TestComposer_OpenAIErrorResponse(t *testing.T) {
	cat := makeCatalogue(5)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]string{"message": "invalid api key"},
		})
	}))
	defer srv.Close()

	c := &Composer{apiKey: "bad-key", model: "gpt-4.1-mini"}

	origClient := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: &testTransport{base: srv.URL}}
	defer func() { http.DefaultClient = origClient }()

	_, err := c.Compose(context.Background(), "a thesis", cat)
	if err == nil {
		t.Fatal("expected error for OpenAI error response")
	}
	if !strings.Contains(err.Error(), "invalid api key") {
		t.Errorf("expected api key error message, got: %v", err)
	}
}

func TestComposer_EmptyChoices(t *testing.T) {
	cat := makeCatalogue(5)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []interface{}{},
		})
	}))
	defer srv.Close()

	c := &Composer{apiKey: "test-key", model: "gpt-4.1-mini"}

	origClient := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: &testTransport{base: srv.URL}}
	defer func() { http.DefaultClient = origClient }()

	_, err := c.Compose(context.Background(), "a thesis", cat)
	if err == nil {
		t.Fatal("expected error for empty choices")
	}
	if !strings.Contains(err.Error(), "empty response") {
		t.Errorf("expected 'empty response' in error, got: %v", err)
	}
}

// testTransport redirects all requests to a test server base URL.
type testTransport struct {
	base string
}

func (t *testTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.URL.Scheme = "http"
	req.URL.Host = strings.TrimPrefix(t.base, "http://")
	return http.DefaultTransport.RoundTrip(req)
}