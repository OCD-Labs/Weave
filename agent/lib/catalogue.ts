// agent/lib/catalogue.ts

import { z } from "zod";
import { CatalogueAsset, CatalogueAssetSchema } from "./validate";

export async function fetchCatalogue(): Promise<CatalogueAsset[]> {
  const backendURL = process.env.BACKEND_URL ?? "http://localhost:8080";

  const response = await fetch(`${backendURL}/catalogue`);
  if (!response.ok) {
    throw new Error(
      `Failed to fetch catalogue from backend: ${response.status} ${response.statusText}`
    );
  }

  const raw    = await response.json();
  const parsed = z.array(CatalogueAssetSchema).safeParse(raw);
  if (!parsed.success) {
    throw new Error(`Invalid catalogue response: ${parsed.error.message}`);
  }

  return parsed.data.filter((a) => a.isActive !== false);
}