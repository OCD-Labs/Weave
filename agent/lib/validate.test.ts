import { describe, it, expect } from "vitest"
import {
  validateProposal,
  stripAndParse,
  CatalogueAssetSchema,
  AIProposalSchema,
  ComposeRequestSchema,
  type CatalogueAsset,
} from "./validate"

const catalogue: CatalogueAsset[] = [
  {
    address: "0x71178bac73cbeb415514eb542a8995b82669778d",
    symbol: "AMD",
    name: "Advanced Micro Devices Inc",
    sector: "Technology",
    currentPriceUsdg: "11000000000",
    isActive: true,
  },
  {
    address: "0x5884ad2f920c162cfbbacc88c9c51aa75ec09e02",
    symbol: "AMZN",
    name: "Amazon.com Inc",
    sector: "Consumer Discretionary",
    currentPriceUsdg: "2050000000000",
    isActive: true,
  },
  {
    address: "0xc9f9c86933092bbbfff3ccb4b105a4a94bf3bd4e",
    symbol: "TSLA",
    name: "Tesla Inc",
    sector: "Consumer Discretionary",
    currentPriceUsdg: "3420000000000",
    isActive: true,
  },
  {
    address: "0x1fbe1a0e43594b3455993b5de5fd0a7a266298d0",
    symbol: "PLTR",
    name: "Palantir Technologies Inc",
    sector: "Technology",
    currentPriceUsdg: "12800000000",
    isActive: true,
  },
  {
    address: "0x3b8262a63d25f0477c4dde23f83cfe22cb768c93",
    symbol: "NFLX",
    name: "Netflix Inc",
    sector: "Communication Services",
    currentPriceUsdg: "123000000000",
    isActive: true,
  },
]

const validProposal = {
  constituents: [
    {
      address: "0x71178bac73cbeb415514eb542a8995b82669778d",
      symbol: "AMD",
      weightBps: 5000,
      rationale: "AMD is a leading semiconductor manufacturer.",
    },
    {
      address: "0x5884ad2f920c162cfbbacc88c9c51aa75ec09e02",
      symbol: "AMZN",
      weightBps: 3000,
      rationale: "Amazon operates massive data centers through AWS.",
    },
    {
      address: "0xc9f9c86933092bbbfff3ccb4b105a4a94bf3bd4e",
      symbol: "TSLA",
      weightBps: 2000,
      rationale: "Tesla builds gigafactories requiring AI infrastructure.",
    },
  ],
  overallRationale: "This basket captures the core physical layer of AI infrastructure.",
  riskNotes: "Concentrated in technology sector with semiconductor supply chain exposure.",
}

describe("validateProposal", () => {
  it("accepts a valid proposal with correct weights summing to 10000", () => {
    const result = validateProposal(validProposal, catalogue)
    expect(result.valid).toBe(true)
  })

  it("rejects when weights do not sum to 10000", () => {
    const bad = {
      ...validProposal,
      constituents: validProposal.constituents.map((c, i) =>
        i === 0 ? { ...c, weightBps: 4000 } : c
      ),
    }
    const result = validateProposal(bad, catalogue)
    expect(result.valid).toBe(false)
    if (!result.valid) {
      expect(result.error).toContain("10000")
    }
  })

  it("rejects when weights sum to more than 10000", () => {
    const bad = {
      ...validProposal,
      constituents: validProposal.constituents.map((c, i) =>
        i === 0 ? { ...c, weightBps: 6000 } : c
      ),
    }
    const result = validateProposal(bad, catalogue)
    expect(result.valid).toBe(false)
  })

  it("rejects constituent address not in catalogue", () => {
    const bad = {
      ...validProposal,
      constituents: [
        ...validProposal.constituents.slice(0, 2),
        {
          address: "0x0000000000000000000000000000000000000000",
          symbol: "FAKE",
          weightBps: 2000,
          rationale: "This address is not in the catalogue.",
        },
      ],
    }
    const result = validateProposal(bad, catalogue)
    expect(result.valid).toBe(false)
    if (!result.valid) {
      expect(result.error).toContain("not found in catalogue")
    }
  })

  it("rejects duplicate constituent addresses", () => {
    const bad = {
      ...validProposal,
      constituents: [
        { ...validProposal.constituents[0], weightBps: 4000 },
        { ...validProposal.constituents[0], weightBps: 4000 }, // duplicate
        { ...validProposal.constituents[2], weightBps: 2000 },
      ],
    }
    const result = validateProposal(bad, catalogue)
    expect(result.valid).toBe(false)
    if (!result.valid) {
      expect(result.error).toContain("Duplicate")
    }
  })

  it("rejects symbol mismatch between proposal and catalogue", () => {
    const bad = {
      ...validProposal,
      constituents: [
        {
          ...validProposal.constituents[0],
          symbol: "WRONGSYMBOL",
        },
        ...validProposal.constituents.slice(1),
      ],
    }
    const result = validateProposal(bad, catalogue)
    expect(result.valid).toBe(false)
    if (!result.valid) {
      expect(result.error).toContain("mismatch")
    }
  })

  it("rejects fewer than 3 constituents", () => {
    const bad = {
      ...validProposal,
      constituents: [
        { ...validProposal.constituents[0], weightBps: 6000 },
        { ...validProposal.constituents[1], weightBps: 4000 },
      ],
    }
    const result = validateProposal(bad, catalogue)
    expect(result.valid).toBe(false)
  })

  it("rejects more than 12 constituents", () => {
    // Build 13 entries — weights won't sum to 10000 so this tests both limits.
    const bad = {
      ...validProposal,
      constituents: Array(13).fill(validProposal.constituents[0]),
    }
    const result = validateProposal(bad, catalogue)
    expect(result.valid).toBe(false)
  })

  it("is case-insensitive on address comparison", () => {
    const mixedCase = {
      ...validProposal,
      constituents: validProposal.constituents.map((c) => ({
        ...c,
        address: c.address.toUpperCase(),
      })),
    }
    const result = validateProposal(mixedCase, catalogue)
    expect(result.valid).toBe(true)
  })

  it("rejects non-object input", () => {
    const result = validateProposal("not an object", catalogue)
    expect(result.valid).toBe(false)
  })

  it("rejects null input", () => {
    const result = validateProposal(null, catalogue)
    expect(result.valid).toBe(false)
  })

  it("rejects missing overallRationale", () => {
    const { overallRationale, ...bad } = validProposal
    const result = validateProposal(bad, catalogue)
    expect(result.valid).toBe(false)
  })

  it("rejects missing riskNotes", () => {
    const { riskNotes, ...bad } = validProposal
    const result = validateProposal(bad, catalogue)
    expect(result.valid).toBe(false)
  })
})

describe("AIProposalSchema weight boundaries", () => {
  it("rejects constituent with weight below 100 bps", () => {
    const bad = {
      ...validProposal,
      constituents: [
        { ...validProposal.constituents[0], weightBps: 50 },
        { ...validProposal.constituents[1], weightBps: 5000 },
        { ...validProposal.constituents[2], weightBps: 4950 },
      ],
    }
    const result = AIProposalSchema.safeParse(bad)
    expect(result.success).toBe(false)
  })

  it("rejects constituent with weight above 5000 bps", () => {
    const bad = {
      ...validProposal,
      constituents: [
        { ...validProposal.constituents[0], weightBps: 5001 },
        { ...validProposal.constituents[1], weightBps: 2500 },
        { ...validProposal.constituents[2], weightBps: 2499 },
      ],
    }
    const result = AIProposalSchema.safeParse(bad)
    expect(result.success).toBe(false)
  })

  it("accepts constituent with weight exactly at 100 bps boundary", () => {
    const edge = {
      ...validProposal,
      constituents: [
        { ...validProposal.constituents[0], weightBps: 100 },
        { ...validProposal.constituents[1], weightBps: 4900 },
        { ...validProposal.constituents[2], weightBps: 5000 },
      ],
    }
    const result = AIProposalSchema.safeParse(edge)
    expect(result.success).toBe(true)
  })

  it("accepts constituent with weight exactly at 5000 bps boundary", () => {
    const edge = {
      ...validProposal,
      constituents: [
        { ...validProposal.constituents[0], weightBps: 5000 },
        { ...validProposal.constituents[1], weightBps: 3000 },
        { ...validProposal.constituents[2], weightBps: 2000 },
      ],
    }
    const result = AIProposalSchema.safeParse(edge)
    expect(result.success).toBe(true)
  })

  it("rejects non-integer weight", () => {
    const bad = {
      ...validProposal,
      constituents: [
        { ...validProposal.constituents[0], weightBps: 2500.5 },
        { ...validProposal.constituents[1], weightBps: 5000 },
        { ...validProposal.constituents[2], weightBps: 2499 },
      ],
    }
    const result = AIProposalSchema.safeParse(bad)
    expect(result.success).toBe(false)
  })
})

describe("stripAndParse", () => {
  it("parses clean JSON", () => {
    const raw = '{"key": "value"}'
    expect(stripAndParse(raw)).toEqual({ key: "value" })
  })

  it("strips markdown json fences", () => {
    const raw = "```json\n{\"key\": \"value\"}\n```"
    expect(stripAndParse(raw)).toEqual({ key: "value" })
  })

  it("strips plain markdown fences", () => {
    const raw = "```\n{\"key\": \"value\"}\n```"
    expect(stripAndParse(raw)).toEqual({ key: "value" })
  })

  it("strips outer escaped quotes", () => {
    const raw = '"{\\\"key\\\": \\\"value\\\"}"'
    const result = stripAndParse(raw)
    expect(result).toEqual({ key: "value" })
  })

  it("handles leading and trailing whitespace", () => {
    const raw = '   \n{"key": "value"}\n   '
    expect(stripAndParse(raw)).toEqual({ key: "value" })
  })

  it("throws on invalid JSON after stripping", () => {
    expect(() => stripAndParse("not json at all")).toThrow()
  })

  it("throws on empty string", () => {
    expect(() => stripAndParse("")).toThrow()
  })

  it("parses a full valid proposal", () => {
    const raw = JSON.stringify(validProposal)
    const result = stripAndParse(raw)
    expect(result).toEqual(validProposal)
  })
})

describe("ComposeRequestSchema", () => {
  it("accepts a valid thesis", () => {
    const result = ComposeRequestSchema.safeParse({
      thesis: "Companies building the physical infrastructure for AI including data centres.",
    })
    expect(result.success).toBe(true)
  })

  it("rejects thesis shorter than 20 characters", () => {
    const result = ComposeRequestSchema.safeParse({ thesis: "too short" })
    expect(result.success).toBe(false)
  })

  it("rejects thesis longer than 2000 characters", () => {
    const result = ComposeRequestSchema.safeParse({ thesis: "a".repeat(2001) })
    expect(result.success).toBe(false)
  })

  it("accepts thesis of exactly 20 characters", () => {
    const result = ComposeRequestSchema.safeParse({ thesis: "a".repeat(20) })
    expect(result.success).toBe(true)
  })

  it("accepts thesis of exactly 2000 characters", () => {
    const result = ComposeRequestSchema.safeParse({ thesis: "a".repeat(2000) })
    expect(result.success).toBe(true)
  })

  it("rejects missing thesis field", () => {
    const result = ComposeRequestSchema.safeParse({})
    expect(result.success).toBe(false)
  })

  it("rejects non-string thesis", () => {
    const result = ComposeRequestSchema.safeParse({ thesis: 12345 })
    expect(result.success).toBe(false)
  })
})

describe("CatalogueAssetSchema", () => {
  it("accepts a valid asset", () => {
    const result = CatalogueAssetSchema.safeParse(catalogue[0])
    expect(result.success).toBe(true)
  })

  it("accepts asset without optional fields", () => {
    const minimal = {
      address: "0x71178bac73cbeb415514eb542a8995b82669778d",
      symbol: "AMD",
      name: "Advanced Micro Devices Inc",
      sector: "Technology",
    }
    const result = CatalogueAssetSchema.safeParse(minimal)
    expect(result.success).toBe(true)
  })

  it("rejects asset missing required address", () => {
    const { address, ...bad } = catalogue[0]
    const result = CatalogueAssetSchema.safeParse(bad)
    expect(result.success).toBe(false)
  })

  it("rejects asset missing required symbol", () => {
    const { symbol, ...bad } = catalogue[0]
    const result = CatalogueAssetSchema.safeParse(bad)
    expect(result.success).toBe(false)
  })
})