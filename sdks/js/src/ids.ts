function getRandomBytes(length: number): Uint8Array {
  try {
    // Works in both browser (crypto.getRandomValues) and Node.js 18+
    const bytes = new Uint8Array(length)
    if (typeof globalThis.crypto !== 'undefined' && globalThis.crypto.getRandomValues) {
      globalThis.crypto.getRandomValues(bytes)
      return bytes
    }
  } catch {
    // fall through to Math.random fallback
  }

  // Fallback for environments without crypto
  const bytes = new Uint8Array(length)
  for (let i = 0; i < length; i++) {
    bytes[i] = Math.floor(Math.random() * 256)
  }
  return bytes
}

function bytesToHex(bytes: Uint8Array): string {
  let hex = ''
  for (let i = 0; i < bytes.length; i++) {
    hex += bytes[i].toString(16).padStart(2, '0')
  }
  return hex
}

/** Generate a 32-character hex trace ID */
export function generateTraceId(): string {
  return bytesToHex(getRandomBytes(16))
}

/** Generate a 16-character hex span ID */
export function generateSpanId(): string {
  return bytesToHex(getRandomBytes(8))
}
