import Anthropic from "@anthropic-ai/sdk";
import express, { Request, Response } from "express";
import dotenv from "dotenv";
import { z } from "zod";

dotenv.config();

// ── Anthropic client ──────────────────────────────────────────────────────────

const anthropic = new Anthropic({
  apiKey: process.env.ANTHROPIC_API_KEY,
});

// ── Zod schemas ───────────────────────────────────────────────────────────────

const CatalogueAssetSchema = z.object({
  address: z.string(),
  symbol: z.string(),
  name: z.string(),
  sector: z.string(),
  currentPriceUsdg: z.string().optional(),
  isActive: z.boolean().optional(),
});

const ProposedConstituentSchema = z.object({
  address: z.string(),
  symbol: z.string(),
  weightBps: z.number().int().min(100).max(5000),
  rationale: z.string().min(1),
});

const AIProposalSchema = z.object({
  constituents: z
    .array(ProposedConstituentSchema)
    .min(3)
    .max(12),
  overallRationale: z.string().min(1),
  riskNotes: z.string().min(1),
});

type CatalogueAsset = z.infer<typeof CatalogueAssetSchema>;
type AIProposal = z.infer<typeof AIProposalSchema>;

const ComposeRequestSchema = z.object({
  thesis: z.string().min(20).max(2000),
});

// ── System prompt ─────────────────────────────────────────────────────────────

function buildSystemPrompt(): string {
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

function buildUserPrompt(thesis: string, catalogue: CatalogueAsset[]): string {
  const catalogueJSON = JSON.stringify(
    catalogue.map((a) => ({
      address: a.address,
      symbol: a.symbol,
      name: a.name,
      sector: a.sector,
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

// ── Validation ────────────────────────────────────────────────────────────────

function validateProposal(
  raw: unknown,
  catalogue: CatalogueAsset[]
): { valid: true; proposal: AIProposal } | { valid: false; error: string } {
  const parsed = AIProposalSchema.safeParse(raw);
  if (!parsed.success) {
    return {
      valid: false,
      error: `Schema validation failed: ${parsed.error.message}`,
    };
  }

  const proposal = parsed.data;
  const catalogueAddresses = new Set(catalogue.map((a) => a.address.toLowerCase()));
  const catalogueByAddress = new Map(catalogue.map((a) => [a.address.toLowerCase(), a]));

  // Verify all proposed addresses exist in the catalogue.
  for (const c of proposal.constituents) {
    if (!catalogueAddresses.has(c.address.toLowerCase())) {
      return {
        valid: false,
        error: `Constituent address ${c.address} (${c.symbol}) not found in catalogue`,
      };
    }

    // Cross-check symbol matches the catalogue entry for that address.
    const catalogueEntry = catalogueByAddress.get(c.address.toLowerCase());
    if (catalogueEntry && catalogueEntry.symbol !== c.symbol) {
      return {
        valid: false,
        error: `Symbol mismatch: address ${c.address} is ${catalogueEntry.symbol} in catalogue but proposal says ${c.symbol}`,
      };
    }
  }

  // Verify no duplicate addresses.
  const addresses = proposal.constituents.map((c) => c.address.toLowerCase());
  const uniqueAddresses = new Set(addresses);
  if (uniqueAddresses.size !== addresses.length) {
    return { valid: false, error: "Duplicate constituent addresses in proposal" };
  }

  // Verify weight sum is exactly 10,000.
  const weightSum = proposal.constituents.reduce((sum, c) => sum + c.weightBps, 0);
  if (weightSum !== 10_000) {
    return {
      valid: false,
      error: `Weight sum is ${weightSum}, must be exactly 10000`,
    };
  }

  return { valid: true, proposal };
}

// ── Anthropic API call with one retry ────────────────────────────────────────

async function callAnthropicWithRetry(
  thesis: string,
  catalogue: CatalogueAsset[]
): Promise<AIProposal> {
  const systemPrompt = buildSystemPrompt();
  const userPrompt = buildUserPrompt(thesis, catalogue);

  async function attemptCall(isRetry: boolean): Promise<AIProposal> {
    const retryNote = isRetry
      ? "\n\nIMPORTANT: Your previous response was not valid JSON or did not meet the schema requirements. Return ONLY the raw JSON object with no markdown, no backticks, no explanation."
      : "";

    const message = await anthropic.messages.create({
      model: "claude-sonnet-4-20250514",
      max_tokens: 1000,
      system: systemPrompt,
      messages: [
        {
          role: "user",
          content: userPrompt + retryNote,
        },
      ],
    });

    const textBlock = message.content.find((b) => b.type === "text");
    if (!textBlock || textBlock.type !== "text") {
      throw new Error("No text content in Anthropic response");
    }

    const rawText = textBlock.text.trim();

    // Strip markdown code fences if the model wrapped the JSON.
    const jsonText = rawText
      .replace(/^```(?:json)?\s*/i, "")
      .replace(/\s*```$/, "")
      .trim();

    let parsed: unknown;
    try {
      parsed = JSON.parse(jsonText);
    } catch {
      throw new Error(`Response is not valid JSON: ${rawText.slice(0, 200)}`);
    }

    const validation = validateProposal(parsed, catalogue);
    if (!validation.valid) {
      throw new Error(validation.error);
    }

    return validation.proposal;
  }

  try {
    return await attemptCall(false);
  } catch (firstError) {
    console.warn(
      `AI composition first attempt failed: ${firstError instanceof Error ? firstError.message : firstError}. Retrying...`
    );
    try {
      return await attemptCall(true);
    } catch (secondError) {
      throw new Error(
        `AI composition failed after retry: ${secondError instanceof Error ? secondError.message : secondError}`
      );
    }
  }
}

// ── Catalogue fetch from Go backend ──────────────────────────────────────────

async function fetchCatalogue(): Promise<CatalogueAsset[]> {
  const backendURL = process.env.BACKEND_URL ?? "http://localhost:8080";

  const response = await fetch(`${backendURL}/catalogue`);
  if (!response.ok) {
    throw new Error(
      `Failed to fetch catalogue from backend: ${response.status} ${response.statusText}`
    );
  }

  const raw = await response.json();
  const parsed = z.array(CatalogueAssetSchema).safeParse(raw);
  if (!parsed.success) {
    throw new Error(`Invalid catalogue response from backend: ${parsed.error.message}`);
  }

  // Only pass active assets to the AI — inactive ones can't be used in baskets.
  return parsed.data.filter((a) => a.isActive !== false);
}

// ── Express server ────────────────────────────────────────────────────────────

const app = express();
app.use(express.json({ limit: "16kb" }));

app.post("/compose", async (req: Request, res: Response): Promise<void> => {
  const bodyParsed = ComposeRequestSchema.safeParse(req.body);
  if (!bodyParsed.success) {
    res.status(400).json({
      error: "Invalid request body",
      details: bodyParsed.error.message,
    });
    return;
  }

  const { thesis } = bodyParsed.data;

  try {
    const catalogue = await fetchCatalogue();

    if (catalogue.length < 3) {
      res.status(503).json({
        error: "Insufficient assets in catalogue to construct a basket (minimum 3 required)",
      });
      return;
    }

    const proposal = await callAnthropicWithRetry(thesis, catalogue);

    // Enrich the proposal with name and sector from the catalogue
    // so the frontend doesn't need a second lookup.
    const catalogueByAddress = new Map(
      catalogue.map((a) => [a.address.toLowerCase(), a])
    );

    const enriched = {
      constituents: proposal.constituents.map((c) => {
        const meta = catalogueByAddress.get(c.address.toLowerCase());
        return {
          address: c.address,
          symbol: c.symbol,
          name: meta?.name ?? "",
          sector: meta?.sector ?? "",
          weightBps: c.weightBps,
          rationale: c.rationale,
          currentPriceUsdg: meta?.currentPriceUsdg ?? "0",
        };
      }),
      overallRationale: proposal.overallRationale,
      riskNotes: proposal.riskNotes,
    };

    res.status(200).json(enriched);
  } catch (error) {
    const message = error instanceof Error ? error.message : "Unknown error";
    console.error(`/compose error: ${message}`);
    res.status(502).json({ error: message });
  }
});

app.get("/health", (_req: Request, res: Response): void => {
  res.status(200).json({ status: "ok", service: "weave-ai-agent" });
});

// ── Start ─────────────────────────────────────────────────────────────────────

const PORT = parseInt(process.env.AI_SERVICE_PORT ?? "3001", 10);

app.listen(PORT, () => {
  console.log(`Weave AI composition engine listening on :${PORT}`);
});

export { callAnthropicWithRetry, validateProposal, fetchCatalogue };