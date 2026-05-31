import { AIProposal, CatalogueAsset, validateProposal, stripAndParse } from "../lib/validate";
import { systemPrompt, userPrompt, retryNote } from "../lib/prompt";

const OPENAI_BASE  = "https://api.openai.com/v1";
const OPENAI_MODEL = process.env.OPENAI_MODEL ?? "gpt-4o";

interface OpenAIResponse {
  choices: { message: { content: string } }[];
}

async function callOnce(prompt: string): Promise<string> {
  const apiKey = process.env.OPENAI_API_KEY;
  if (!apiKey) throw new Error("OPENAI_API_KEY not set in .env");

  const response = await fetch(`${OPENAI_BASE}/chat/completions`, {
    method:  "POST",
    headers: {
      "Content-Type":  "application/json",
      "Authorization": `Bearer ${apiKey}`,
    },
    body: JSON.stringify({
      model:       OPENAI_MODEL,
      max_tokens:  1500,
      temperature: 0,
      messages:    [
        { role: "system", content: systemPrompt() },
        { role: "user",   content: prompt },
      ],
    }),
  });

  if (!response.ok) {
    const text = await response.text();
    throw new Error(`OpenAI error ${response.status}: ${text}`);
  }

  const data = (await response.json()) as OpenAIResponse;
  const content = data.choices[0]?.message?.content;
  if (!content) throw new Error("Empty content in OpenAI response");
  return content;
}

export async function callOpenAI(
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
    console.warn(`OpenAI first attempt failed: ${firstErr}. Retrying...`);
  }

  const raw       = await callOnce(prompt + retryNote());
  const parsed    = stripAndParse(raw);
  const validated = validateProposal(parsed, catalogue);
  if (!validated.valid) throw new Error(`OpenAI retry failed: ${validated.error}`);
  return validated.proposal;
}