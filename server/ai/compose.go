package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// CatalogueAsset is the shape the AI composer receives per stock.
type CatalogueAsset struct {
	Address          string `json:"address"`
	Symbol           string `json:"symbol"`
	Name             string `json:"name"`
	Sector           string `json:"sector"`
	CurrentPriceUsdg string `json:"currentPriceUsdg"`
}

// ProposedConstituent is one stock in the AI's returned basket proposal.
type ProposedConstituent struct {
	Address          string `json:"address"`
	Symbol           string `json:"symbol"`
	Name             string `json:"name"`
	Sector           string `json:"sector"`
	WeightBps        int    `json:"weightBps"`
	Rationale        string `json:"rationale"`
	CurrentPriceUsdg string `json:"currentPriceUsdg"`
}

// Proposal is the full AI composition response returned to the caller.
type Proposal struct {
	Constituents     []ProposedConstituent `json:"constituents"`
	OverallRationale string                `json:"overallRationale"`
	RiskNotes        string                `json:"riskNotes"`
	Provider         string                `json:"provider"`
}

// Composer calls the OpenAI chat completions API and validates the response.
type Composer struct {
	apiKey string
	model  string
}

func NewComposer(apiKey, model string) *Composer {
	return &Composer{apiKey: apiKey, model: model}
}

// Compose sends the thesis and catalogue to OpenAI and returns a validated proposal.
// Retries once with a stricter prompt if the first response fails validation.
func (c *Composer) Compose(ctx context.Context, thesis string, catalogue []CatalogueAsset) (*Proposal, error) {
	raw, err := c.call(ctx, systemPrompt(), userPrompt(thesis, catalogue))
	if err != nil {
		return nil, fmt.Errorf("openai call: %w", err)
	}

	proposal, err := stripAndValidate(raw, catalogue)
	if err != nil {
		// Retry once with a stricter JSON-only reminder.
		raw, err = c.call(ctx, systemPrompt(), userPrompt(thesis, catalogue)+retryNote())
		if err != nil {
			return nil, fmt.Errorf("openai retry call: %w", err)
		}
		proposal, err = stripAndValidate(raw, catalogue)
		if err != nil {
			return nil, fmt.Errorf("validation failed after retry: %w", err)
		}
	}

	// Enrich with name, sector, price from catalogue.
	byAddr := make(map[string]CatalogueAsset, len(catalogue))
	for _, a := range catalogue {
		byAddr[strings.ToLower(a.Address)] = a
	}

	for i, c := range proposal.Constituents {
		if meta, ok := byAddr[strings.ToLower(c.Address)]; ok {
			proposal.Constituents[i].Name             = meta.Name
			proposal.Constituents[i].Sector           = meta.Sector
			proposal.Constituents[i].CurrentPriceUsdg = meta.CurrentPriceUsdg
		}
	}

	proposal.Provider = "openai/" + c.model
	return proposal, nil
}

// openAIRequest is the request body shape for /v1/chat/completions.
type openAIRequest struct {
	Model    string              `json:"model"`
	Messages []openAIMessage     `json:"messages"`
	ResponseFormat *responseFormat `json:"response_format,omitempty"`
}

type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type responseFormat struct {
	Type string `json:"type"`
}

type openAIResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (c *Composer) call(ctx context.Context, system, user string) (string, error) {
	body, err := json.Marshal(openAIRequest{
		Model: c.model,
		Messages: []openAIMessage{
			{Role: "system", Content: system},
			{Role: "user",   Content: user},
		},
		ResponseFormat: &responseFormat{Type: "json_object"},
	})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.openai.com/v1/chat/completions",
		bytes.NewReader(body),
	)
	if err != nil {
		return "", err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read body: %w", err)
	}

	var oResp openAIResponse
	if err := json.Unmarshal(raw, &oResp); err != nil {
		return "", fmt.Errorf("unmarshal: %w", err)
	}

	if oResp.Error != nil {
		return "", fmt.Errorf("openai error: %s", oResp.Error.Message)
	}

	if len(oResp.Choices) == 0 || oResp.Choices[0].Message.Content == "" {
		return "", fmt.Errorf("empty response from openai")
	}

	return oResp.Choices[0].Message.Content, nil
}

func systemPrompt() string {
	return `You are a financial analyst building thematic equity baskets from a specific catalogue of tokenized stocks. You will receive a natural language investment thesis and a JSON catalogue of available stocks with their symbols, names, sectors, and current prices.

Your task is to select 3 to 12 stocks from the catalogue that best represent the thesis, assign each a target weight in basis points that sum to exactly 10,000, and provide a one-sentence rationale for each inclusion.

Rules:
- Only select stocks that appear in the provided catalogue
- Weights must be integers and must sum to exactly 10,000
- No single weight may be less than 100 (1%) or more than 5000 (50%)
- Do not include more than 12 constituents
- Do not include fewer than 3 constituents
- Prefer direct plays over indirect beneficiaries unless the thesis specifically calls for breadth
- Weight by conviction and relevance to the thesis, not by market cap alone

Return ONLY valid JSON matching this schema, no preamble or explanation outside the JSON:
{
  "constituents": [
    {
      "address": "0x...",
      "symbol": "AAPL",
      "weightBps": 2000,
      "rationale": "one sentence explaining why this stock fits the thesis"
    }
  ],
  "overallRationale": "two to three sentences explaining the basket overall construction logic",
  "riskNotes": "one to two sentences noting the key risks or concentration exposures"
}`
}

func userPrompt(thesis string, catalogue []CatalogueAsset) string {
	catJSON, _ := json.MarshalIndent(catalogue, "", "  ")
	return fmt.Sprintf("Investment thesis: %s\n\nAvailable stock catalogue:\n%s\n\nReturn only the JSON proposal with no additional text.", thesis, catJSON)
}

func retryNote() string {
	return "\n\nIMPORTANT: Your previous response was not valid JSON or did not meet schema requirements. Return ONLY the raw JSON object. No markdown, no backticks, no explanation."
}

// rawProposal is the shape we parse from the LLM before validation.
type rawProposal struct {
	Constituents []struct {
		Address   string `json:"address"`
		Symbol    string `json:"symbol"`
		WeightBps int    `json:"weightBps"`
		Rationale string `json:"rationale"`
	} `json:"constituents"`
	OverallRationale string `json:"overallRationale"`
	RiskNotes        string `json:"riskNotes"`
}

// stripAndValidate strips markdown fences, parses JSON, and validates
// all business rules.
func stripAndValidate(raw string, catalogue []CatalogueAsset) (*Proposal, error) {
	clean := raw
	clean = strings.TrimPrefix(clean, "```json")
	clean = strings.TrimPrefix(clean, "```")
	clean = strings.TrimSuffix(clean, "```")
	clean = strings.Trim(clean, `"`)
	clean = strings.ReplaceAll(clean, `\"`, `"`)
	clean = strings.TrimSpace(clean)

	var rp rawProposal
	if err := json.Unmarshal([]byte(clean), &rp); err != nil {
		return nil, fmt.Errorf("json parse: %w", err)
	}

	if len(rp.Constituents) < 3 {
		return nil, fmt.Errorf("too few constituents: %d (minimum 3)", len(rp.Constituents))
	}
	if len(rp.Constituents) > 12 {
		return nil, fmt.Errorf("too many constituents: %d (maximum 12)", len(rp.Constituents))
	}
	if strings.TrimSpace(rp.OverallRationale) == "" {
		return nil, fmt.Errorf("overallRationale is empty")
	}
	if strings.TrimSpace(rp.RiskNotes) == "" {
		return nil, fmt.Errorf("riskNotes is empty")
	}

	byAddr := make(map[string]CatalogueAsset, len(catalogue))
	for _, a := range catalogue {
		byAddr[strings.ToLower(a.Address)] = a
	}

	seen    := make(map[string]bool, len(rp.Constituents))
	weightSum := 0

	for _, c := range rp.Constituents {
		addr := strings.ToLower(c.Address)

		meta, ok := byAddr[addr]
		if !ok {
			return nil, fmt.Errorf("address %s (%s) not in catalogue", c.Address, c.Symbol)
		}
		if meta.Symbol != c.Symbol {
			return nil, fmt.Errorf("symbol mismatch at %s: catalogue has %s, proposal has %s", c.Address, meta.Symbol, c.Symbol)
		}
		if seen[addr] {
			return nil, fmt.Errorf("duplicate constituent: %s", c.Address)
		}
		seen[addr] = true

		if c.WeightBps < 100 {
			return nil, fmt.Errorf("weight too low for %s: %d bps (minimum 100)", c.Symbol, c.WeightBps)
		}
		if c.WeightBps > 5000 {
			return nil, fmt.Errorf("weight too high for %s: %d bps (maximum 5000)", c.Symbol, c.WeightBps)
		}
		if strings.TrimSpace(c.Rationale) == "" {
			return nil, fmt.Errorf("empty rationale for %s", c.Symbol)
		}

		weightSum += c.WeightBps
	}

	if weightSum != 10000 {
		return nil, fmt.Errorf("weight sum is %d, must be exactly 10000", weightSum)
	}

	constituents := make([]ProposedConstituent, len(rp.Constituents))
	for i, c := range rp.Constituents {
		constituents[i] = ProposedConstituent{
			Address:   c.Address,
			Symbol:    c.Symbol,
			WeightBps: c.WeightBps,
			Rationale: c.Rationale,
		}
	}

	return &Proposal{
		Constituents:     constituents,
		OverallRationale: rp.OverallRationale,
		RiskNotes:        rp.RiskNotes,
	}, nil
}