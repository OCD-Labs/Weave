import { describe, it, expect } from "vitest"
import { systemPrompt, userPrompt, retryNote } from "./prompt"
import type { CatalogueAsset } from "./validate"

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
]

describe("systemPrompt", () => {
  it("returns a non-empty string", () => {
    const prompt = systemPrompt()
    expect(typeof prompt).toBe("string")
    expect(prompt.length).toBeGreaterThan(100)
  })

  it("contains the JSON schema instructions", () => {
    const prompt = systemPrompt()
    expect(prompt).toContain("constituents")
    expect(prompt).toContain("weightBps")
    expect(prompt).toContain("10,000")
    expect(prompt).toContain("rationale")
  })

  it("contains weight boundary rules", () => {
    const prompt = systemPrompt()
    expect(prompt).toContain("100")
    expect(prompt).toContain("5000")
  })

  it("is deterministic across multiple calls", () => {
    expect(systemPrompt()).toBe(systemPrompt())
  })

  it("instructs the model to return only JSON", () => {
    const prompt = systemPrompt()
    expect(prompt.toLowerCase()).toContain("only")
    expect(prompt.toLowerCase()).toContain("json")
  })
})

describe("userPrompt", () => {
  it("includes the thesis text", () => {
    const thesis = "Companies building AI infrastructure in Southeast Asia"
    const prompt = userPrompt(thesis, catalogue)
    expect(prompt).toContain(thesis)
  })

  it("includes all catalogue asset symbols", () => {
    const prompt = userPrompt("AI infrastructure thesis", catalogue)
    expect(prompt).toContain("AMD")
    expect(prompt).toContain("AMZN")
  })

  it("includes asset addresses in the catalogue JSON", () => {
    const prompt = userPrompt("AI infrastructure thesis", catalogue)
    expect(prompt).toContain("0x71178bac73cbeb415514eb542a8995b82669778d")
    expect(prompt).toContain("0x5884ad2f920c162cfbbacc88c9c51aa75ec09e02")
  })

  it("includes sector information", () => {
    const prompt = userPrompt("AI infrastructure thesis", catalogue)
    expect(prompt).toContain("Technology")
    expect(prompt).toContain("Consumer Discretionary")
  })

  it("handles empty catalogue without throwing", () => {
    expect(() => userPrompt("some thesis", [])).not.toThrow()
  })

  it("handles special characters in thesis without throwing", () => {
    const thesis = 'Thesis with "quotes" and <tags> and & ampersands'
    expect(() => userPrompt(thesis, catalogue)).not.toThrow()
  })

  it("includes price information", () => {
    const prompt = userPrompt("AI thesis", catalogue)
    expect(prompt).toContain("11000000000")
  })
})

describe("retryNote", () => {
  it("returns a non-empty string", () => {
    expect(retryNote().length).toBeGreaterThan(0)
  })

  it("emphasises JSON requirement", () => {
    const note = retryNote().toLowerCase()
    expect(note).toContain("json")
  })

  it("is deterministic", () => {
    expect(retryNote()).toBe(retryNote())
  })
})