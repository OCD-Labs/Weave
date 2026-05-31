import { AIProposal, CatalogueAsset } from "../lib/validate";
import { callOllama }    from "./ollama";
import { callAnthropic } from "./anthropic";
import { callOpenAI }    from "./openai";

export type LLMProvider = "ollama" | "anthropic" | "openai";

export const PROVIDER = (process.env.LLM_PROVIDER ?? "openai") as LLMProvider;

export async function callLLM(
  thesis: string,
  catalogue: CatalogueAsset[]
): Promise<AIProposal> {
  switch (PROVIDER) {
    case "ollama":
      return callOllama(thesis, catalogue);
    case "anthropic":
      return callAnthropic(thesis, catalogue);
    case "openai":
      return callOpenAI(thesis, catalogue);
    default:
      throw new Error(`Unknown LLM_PROVIDER: "${PROVIDER}". Valid options: ollama, anthropic, openai`);
  }
}