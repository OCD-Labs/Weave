// agent/compose.ts

import express, { Request, Response } from "express";
import dotenv from "dotenv";

import { ComposeRequestSchema, CatalogueAsset } from "./lib/validate";
import { fetchCatalogue }                        from "./lib/catalogue";
import { callLLM, PROVIDER }                     from "./providers/index";

dotenv.config();

const app = express();
app.use(express.json({ limit: "16kb" }));

// ── POST /compose ─────────────────────────────────────────────────────────────

app.post("/compose", async (req: Request, res: Response): Promise<void> => {
  const bodyParsed = ComposeRequestSchema.safeParse(req.body);
  if (!bodyParsed.success) {
    res.status(400).json({ error: "Invalid request body", details: bodyParsed.error.message });
    return;
  }

  const { thesis } = bodyParsed.data;

  try {
    const catalogue = await fetchCatalogue();

    if (catalogue.length < 3) {
      res.status(503).json({
        error: "Insufficient active assets in catalogue (minimum 3 required)",
      });
      return;
    }

    const proposal          = await callLLM(thesis, catalogue);
    const catalogueByAddress = new Map<string, CatalogueAsset>(
      catalogue.map((a) => [a.address.toLowerCase(), a])
    );

    // Enrich the proposal with name, sector, and price from the catalogue
    // so the frontend doesn't need a second lookup per constituent.
    const enriched = {
      constituents: proposal.constituents.map((c) => {
        const meta = catalogueByAddress.get(c.address.toLowerCase());
        return {
          address:          c.address,
          symbol:           c.symbol,
          name:             meta?.name             ?? "",
          sector:           meta?.sector           ?? "",
          weightBps:        c.weightBps,
          rationale:        c.rationale,
          currentPriceUsdg: meta?.currentPriceUsdg ?? "0",
        };
      }),
      overallRationale: proposal.overallRationale,
      riskNotes:        proposal.riskNotes,
      provider:         PROVIDER,
    };

    res.status(200).json(enriched);
  } catch (error) {
    const message = error instanceof Error ? error.message : "Unknown error";
    console.error(`/compose error: ${message}`);
    res.status(502).json({ error: message });
  }
});

// ── GET /health ───────────────────────────────────────────────────────────────

app.get("/health", (_req: Request, res: Response): void => {
  res.status(200).json({ status: "ok", service: "weave-ai-agent", provider: PROVIDER });
});

// ── Start ─────────────────────────────────────────────────────────────────────

const PORT = parseInt(process.env.AI_SERVICE_PORT ?? "3001", 10);

app.listen(PORT, () => {
  console.log(`Weave AI agent listening on :${PORT} — provider: ${PROVIDER}`);
});

export {};