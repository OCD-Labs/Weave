import { CatalogueAsset } from "./validate";

export function systemPrompt(): string {
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
}`;
}

export function userPrompt(thesis: string, catalogue: CatalogueAsset[]): string {
  const catalogueJSON = JSON.stringify(
    catalogue.map((a) => ({
      address:          a.address,
      symbol:           a.symbol,
      name:             a.name,
      sector:           a.sector,
      currentPriceUsdg: a.currentPriceUsdg ?? "unknown",
    })),
    null,
    2
  );

  return `Investment thesis: ${thesis}

Available stock catalogue:
${catalogueJSON}

Return only the JSON proposal with no additional text.`;
}

export function retryNote(): string {
  return "\n\nIMPORTANT: Your previous response was not valid JSON or did not meet schema requirements. Return ONLY the raw JSON object. No markdown, no backticks, no explanation.";
}