import type { SpanData } from "./types.js";

export class Transport {
  private endpoint: string;
  private apiKey: string;

  constructor(endpoint: string, apiKey: string) {
    this.endpoint = endpoint.replace(/\/+$/, "");
    this.apiKey = apiKey;
  }

  async send(spans: SpanData[]): Promise<boolean> {
    try {
      const response = await fetch(`${this.endpoint}/api/v1/spans`, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          "X-SpanBarn-Api-Key": this.apiKey,
        },
        body: JSON.stringify({ spans }),
      });
      return response.ok;
    } catch {
      return false;
    }
  }
}
