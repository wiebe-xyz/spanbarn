import type { CSSProperties } from 'react'

export const selectStyle: CSSProperties = {
  background: '#1f2937',
  border: '1px solid #374151',
  borderRadius: 6,
  padding: '6px 10px',
  color: '#e5e7eb',
  fontSize: 13,
}

export const inputStyle: CSSProperties = { ...selectStyle, minWidth: 0 }

export const buttonStyle: CSSProperties = {
  background: '#1f2937',
  border: '1px solid #374151',
  borderRadius: 6,
  color: '#e5e7eb',
  padding: '6px 12px',
  fontSize: 13,
  cursor: 'pointer',
}

export const primaryButton: CSSProperties = { ...buttonStyle, background: '#2563eb', borderColor: '#2563eb', color: '#fff' }

export const iconButton: CSSProperties = { ...buttonStyle, padding: '2px 8px', fontSize: 12 }

export const errorStyle: CSSProperties = {
  padding: '8px 12px',
  background: 'rgba(239,68,68,0.1)',
  border: '1px solid #ef4444',
  borderRadius: 6,
  color: '#ef4444',
  marginBottom: 12,
  fontSize: 13,
}

export const mutedText: CSSProperties = { color: '#9ca3af', fontSize: 13 }
