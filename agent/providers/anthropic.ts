import Anthropic from "@anthropic-ai/sdk";
import { AIProposal, CatalogueAsset, validateProposal, stripAndParse } from "../lib/validate";
import { systemPrompt, userPrompt, retryNote } from "../lib/prompt";

const client = new Anthropic({ apiKey: process.env.ANTHROPIC_API_KEY });

async function callOnce(prompt: string): Promise<string> {
  const message = await client.messages.create({
    model:      "claude-sonnet-4-20250514",
    max_tokens: 1500,
    system:     systemPrompt(),
    messages:   [{ role: "user", content: prompt }],
  });

  const textBlock = message.content.find((b) => b.type === "text");
  if (!textBlock || textBlock.type !== "text") {
    throw new Error("No text block in Anthropic response");
  }
  return textBlock.text;
}

export async function callAnthropic(
  thesis: string,
  catalogue: CatalogueAsset[]
): Promise<AIProposal> {
  const prompt = userPrompt(thesis, catalogue);

  try {
    const raw       = await callOnce(prompt);
    const parsed    = stripAndParse(raw);
    const validated = validateProposal(parsed, catalogue);
    if (!validated.valid) throw new Error(validated.error);
    return validated.proposal;
  } catch (firstErr) {
    console.warn(`Anthropic first attempt failed: ${firstErr}. Retrying...`);
  }

  const raw       = await callOnce(prompt + retryNote());
  const parsed    = stripAndParse(raw);
  const validated = validateProposal(parsed, catalogue);
  if (!validated.valid) throw new Error(`Anthropic retry failed: ${validated.error}`);
  return validated.proposal;
}