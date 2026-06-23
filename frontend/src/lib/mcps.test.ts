import { describe, it, expect } from 'vitest'
import { mcpCatalog, mcpIcon } from './mcps'

// Static catalog of MCPs (frontend-only, hardcoded data): it feeds the
// marketplace-style view inside settings. Independent of the backend.
describe('mcpCatalog', () => {
  it('exposes at least one entry with name and description', () => {
    expect(mcpCatalog.length).toBeGreaterThan(0)
    for (const entry of mcpCatalog) {
      expect(entry.name.length).toBeGreaterThan(0)
      expect(entry.description.length).toBeGreaterThan(0)
    }
  })

  it('every entry has a unique id', () => {
    const ids = mcpCatalog.map((entry) => entry.id)
    expect(new Set(ids).size).toBe(ids.length)
  })

  it('the GitHub entry brings its own image', () => {
    const github = mcpCatalog.find((entry) => entry.id === 'github')
    expect(github?.image).toBeTruthy()
  })
})

describe('mcpIcon', () => {
  it('generates a self-contained SVG data URI with the MCP initial', () => {
    const uri = mcpIcon({ id: 'gh', name: 'Github', description: 'x', accent: '#1c1c1a' })
    expect(uri.startsWith('data:image/svg+xml,')).toBe(true)
    expect(decodeURIComponent(uri)).toContain('>G<')
  })
})
