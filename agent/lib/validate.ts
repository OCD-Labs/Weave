// agent/lib/validate.ts

import { z } from "zod";

export const CatalogueAssetSchema = z.object({
  address:          z.string(),
  symbol:           z.string(),
  name:             z.string(),
  sector:           z.string(),
  currentPriceUsdg: z.string().optional(),
  isActive:         z.boolean().optional(),
});

export const ProposedConstituentSchema = z.object({
  address:   z.string(),
  symbol:    z.string(),
  weightBps: z.number().int().min(100).max(5000),
  rationale: z.string().min(1),
});

export const AIProposalSchema = z.object({
  constituents:     z.array(ProposedConstituentSchema).min(3).max(12),
  overallRationale: z.string().min(1),
  riskNotes:        z.string().min(1),
});

export const ComposeRequestSchema = z.object({
  thesis: z.string().min(20).max(2000),
});

export type CatalogueAsset = z.infer<typeof CatalogueAssetSchema>;
export type AIProposal     = z.infer<typeof AIProposalSchema>;

export function validateProposal(
  raw: unknown,
  catalogue: CatalogueAsset[]
): { valid: true; proposal: AIProposal } | { valid: false; error: string } {
  const parsed = AIProposalSchema.safeParse(raw);
  if (!parsed.success) {
    return { valid: false, error: `Schema validation failed: ${parsed.error.message}` };
  }

  const proposal           = parsed.data;
  const byAddress          = new Map(catalogue.map((a) => [a.address.toLowerCase(), a]));
  const catalogueAddresses = new Set(byAddress.keys());

  for (const c of proposal.constituents) {
    if (!catalogueAddresses.has(c.address.toLowerCase())) {
      return { valid: false, error: `Address ${c.address} (${c.symbol}) not found in catalogue` };
    }
    const entry = byAddress.get(c.address.toLowerCase());
    if (entry && entry.symbol !== c.symbol) {
      return {
        valid: false,
        error: `Symbol mismatch at ${c.address}: catalogue has ${entry.symbol}, proposal has ${c.symbol}`,
      };
    }
  }

  const addresses = proposal.constituents.map((c) => c.address.toLowerCase());
  if (new Set(addresses).size !== addresses.length) {
    return { valid: false, error: "Duplicate constituent addresses in proposal" };
  }

  const weightSum = proposal.constituents.reduce((s, c) => s + c.weightBps, 0);
  if (weightSum !== 10_000) {
    return { valid: false, error: `Weight sum is ${weightSum}, must be exactly 10000` };
  }

  return { valid: true, proposal };
}

// Strips markdown fences and escaped quotes some models add around JSON.
export function stripAndParse(raw: string): unknown {
  const clean = raw
    .replace(/^```(?:json)?\s*/i, "")
    .replace(/\s*```$/, "")
    .replace(/^"|"$/g, "")
    .replace(/\\"/g, '"')
    .trim();
  return JSON.parse(clean);
}