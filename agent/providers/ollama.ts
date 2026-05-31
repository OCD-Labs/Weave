// agent/providers/ollama.ts
// Free local LLM via Ollama — install at https://ollama.com then: ollama pull llama3.2
// API docs confirmed at https://ollama.com/api — POST /api/chat, stream: false

import { AIProposal, CatalogueAsset, validateProposal, stripAndParse } from "../lib/validate";
import { systemPrompt, userPrompt, retryNote } from "../lib/prompt";

const OLLAMA_BASE = process.env.OLLAMA_BASE_URL ?? "http://localhost:11434";
const OLLAMA_MODEL = process.env.OLLAMA_MODEL ?? "llama3.2";

interface OllamaChatResponse {
  message: { role: string; content: string };
  done: boolean;
}

async function callOnce(prompt: string): Promise<string> {
  const response = await fetch(`${OLLAMA_BASE}/api/chat`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      model:    OLLAMA_MODEL,
      stream:   false,
      messages: [
        { role: "system",  content: systemPrompt() },
        { role: "user",    content: prompt },
      ],
      options: {
        temperature: 0,    // deterministic output — critical for JSON reliability
        num_predict: 1500,
      },
    }),
  });

  if (!response.ok) {
    const text = await response.text();
    throw new Error(`Ollama error ${response.status}: ${text}`);
  }

  const data = (await response.json()) as OllamaChatResponse;
  return data.message.content;
}

export async function callOllama(
  thesis: string,
  catalogue: CatalogueAsset[]
): Promise<AIProposal> {
  const prompt = userPrompt(thesis, catalogue);

  // First attempt.
  try {
    const raw       = await callOnce(prompt);
    const parsed    = stripAndParse(raw);
    const validated = validateProposal(parsed, catalogue);
    if (!validated.valid) throw new Error(validated.error);
    return validated.proposal;
  } catch (firstErr) {
    console.warn(`Ollama first attempt failed: ${firstErr}. Retrying with stricter prompt...`);
  }

  // Single retry with explicit JSON reminder.
  const raw       = await callOnce(prompt + retryNote());
  const parsed    = stripAndParse(raw);
  const validated = validateProposal(parsed, catalogue);
  if (!validated.valid) throw new Error(`Ollama retry failed: ${validated.error}`);
  return validated.proposal;
}