import { describe, expect, it } from 'vitest'
import { headersText, parseHeaders, presetOf } from './SherlockSettings'

describe('Sherlock provider settings', () => {
  it('names the preset a saved provider came from', () => {
    expect(presetOf(undefined)).toBe('anthropic')
    expect(presetOf({ kind: 'anthropic', endpoint: 'https://api.anthropic.com' })).toBe('anthropic')
    expect(presetOf({ kind: 'anthropic', endpoint: 'https://ai-gateway.corp.example/anthropic' })).toBe('anthropic-gateway')
    expect(presetOf({ kind: 'openai' })).toBe('openai')
    expect(presetOf({ kind: 'openai', endpoint: 'https://shop.openai.azure.com/openai/deployments/gpt', authHeader: 'api-key', authPrefix: '' })).toBe('azure')
    expect(presetOf({ kind: 'openai', endpoint: 'https://ai-gateway.corp.example/v1' })).toBe('compatible')
    expect(presetOf({ kind: 'bedrock', region: 'eu-west-1' })).toBe('bedrock')
    expect(presetOf({ kind: 'vertex', region: 'us-east5', project: 'p' })).toBe('vertex')
  })

  it('edits extra headers as lines', () => {
    expect(parseHeaders('X-Team: platform\n\n bad line\nX-Route:  a:b ')).toEqual({ 'X-Team': 'platform', 'X-Route': 'a:b' })
    expect(parseHeaders('')).toBeUndefined()
    expect(headersText({ 'X-Team': 'platform', 'X-Cost': '1' })).toBe('X-Team: platform\nX-Cost: 1')
  })
})
