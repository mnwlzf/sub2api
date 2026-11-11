import { describe, expect, it } from 'vitest'
import {
  MAGPIE_MAX_MODELS,
  buildMagpieBatchScript,
  buildMagpieImportDeeplink,
  limitMagpieModels,
  magpieBalanceUrl,
  magpieEndpointBaseUrl,
  magpieProviderId,
  magpieProviderRoot,
  resolveMagpieEndpoints,
  resolveMagpieProtocols
} from '@/utils/magpieImport'

function paramsFromDeeplink(deeplink: string): URLSearchParams {
  const query = deeplink.split('?')[1] || ''
  return new URLSearchParams(query)
}

const HOST = 'https://api.example.com'

describe('magpieImport protocol selection', () => {
  it('registers the native protocol plus chat for each platform', () => {
    expect(resolveMagpieProtocols('anthropic')).toEqual(['anthropic', 'chat'])
    expect(resolveMagpieProtocols('openai')).toEqual(['responses', 'chat'])
    expect(resolveMagpieProtocols('gemini')).toEqual(['gemini', 'chat'])
    expect(resolveMagpieProtocols('antigravity')).toEqual(['anthropic', 'gemini', 'chat'])
  })

  it('falls back to chat for platforms magpie has no native protocol for', () => {
    expect(resolveMagpieProtocols('grok')).toEqual(['chat'])
    expect(resolveMagpieProtocols('typesafe')).toEqual(['chat'])
    expect(resolveMagpieProtocols(undefined)).toEqual(['chat'])
  })

  it('keeps chat/responses on /v1 and anthropic/gemini on the bare root', () => {
    // 实测依据：magpie 会探测 <base>/models（chat/responses）、
    // <base>/models 与 <base>/v1/models（anthropic）、<base>/v1beta/models（gemini）。
    expect(magpieEndpointBaseUrl('chat', HOST)).toBe(`${HOST}/v1`)
    expect(magpieEndpointBaseUrl('responses', HOST)).toBe(`${HOST}/v1`)
    expect(magpieEndpointBaseUrl('anthropic', HOST)).toBe(HOST)
    expect(magpieEndpointBaseUrl('gemini', HOST)).toBe(HOST)
  })

  it('moves antigravity groups under the /antigravity prefix', () => {
    expect(magpieProviderRoot(HOST, 'antigravity')).toBe(`${HOST}/antigravity`)
    expect(magpieProviderRoot(`${HOST}/`, 'antigravity')).toBe(`${HOST}/antigravity`)
    expect(magpieProviderRoot(HOST, 'anthropic')).toBe(HOST)
    expect(resolveMagpieEndpoints({ baseUrl: HOST, platform: 'antigravity' })).toEqual([
      { protocol: 'anthropic', baseUrl: `${HOST}/antigravity` },
      { protocol: 'gemini', baseUrl: `${HOST}/antigravity` },
      { protocol: 'chat', baseUrl: `${HOST}/antigravity/v1` }
    ])
  })
})

describe('buildMagpieImportDeeplink', () => {
  it('builds an anthropic + chat provider for a Claude Code group', () => {
    const link = buildMagpieImportDeeplink({
      baseUrl: HOST,
      platform: 'anthropic',
      providerId: 'sub2api-65',
      providerName: 'Claude 分组',
      apiKey: 'sk-abc'
    })

    expect(link.startsWith('magpie://import?')).toBe(true)
    const params = paramsFromDeeplink(link)
    expect(params.get('name')).toBe('Claude 分组')
    expect(params.get('id')).toBe('sub2api-65')
    expect(params.get('anthropic')).toBe(HOST)
    expect(params.get('chat')).toBe(`${HOST}/v1`)
    expect(params.get('key')).toBe('sk-abc')
    // 非 antigravity 平台不应带 responses/gemini 端点
    expect(params.get('responses')).toBeNull()
    expect(params.get('gemini')).toBeNull()
  })

  it('builds responses + chat for an OpenAI group', () => {
    const params = paramsFromDeeplink(
      buildMagpieImportDeeplink({
        baseUrl: HOST,
        platform: 'openai',
        providerName: 'Fast',
        apiKey: 'sk-openai'
      })
    )
    expect(params.get('responses')).toBe(`${HOST}/v1`)
    expect(params.get('chat')).toBe(`${HOST}/v1`)
    expect(params.get('anthropic')).toBeNull()
  })

  it('wires the balance query to sub2api /v1/usage with remaining', () => {
    const params = paramsFromDeeplink(
      buildMagpieImportDeeplink({
        baseUrl: HOST,
        platform: 'antigravity',
        providerName: 'UltraFast',
        apiKey: 'sk-bal',
        includeBalance: true
      })
    )
    expect(magpieBalanceUrl({ baseUrl: HOST, platform: 'antigravity' })).toBe(
      `${HOST}/antigravity/v1/usage`
    )
    expect(params.get('balance')).toBe(`${HOST}/antigravity/v1/usage`)
    expect(params.get('balance.path')).toBe('remaining')
  })

  it('carries declared models and drops duplicates', () => {
    const params = paramsFromDeeplink(
      buildMagpieImportDeeplink({
        baseUrl: HOST,
        platform: 'openai',
        providerName: 'Relay',
        apiKey: 'sk-m',
        models: ['gpt-6.1-sol', 'gpt-6.1-sol', ' gpt-6.1-terra ']
      })
    )
    expect(params.get('models')).toBe('gpt-6.1-sol,gpt-6.1-terra')
  })

  it('keeps the capped model list while the link still fits', () => {
    const many = Array.from({ length: MAGPIE_MAX_MODELS }, (_, i) => `model-${i}`)
    const params = paramsFromDeeplink(
      buildMagpieImportDeeplink({
        baseUrl: HOST,
        platform: 'openai',
        providerName: 'Relay',
        apiKey: 'sk-ok',
        models: many
      })
    )
    expect(params.get('models')?.split(',')).toHaveLength(MAGPIE_MAX_MODELS)
  })

  it('omits models when the link would exceed the safe length', () => {
    // 每个名字 100 字符，20 个必然把链接顶过 MAGPIE_MAX_LINK_LENGTH
    const many = Array.from({ length: MAGPIE_MAX_MODELS }, (_, i) => `${'m'.repeat(98)}${i}`)
    const link = buildMagpieImportDeeplink({
      baseUrl: HOST,
      platform: 'openai',
      providerName: 'Relay',
      apiKey: 'sk-long',
      models: many
    })
    expect(paramsFromDeeplink(link).get('models')).toBeNull()
    expect(paramsFromDeeplink(link).get('chat')).toBe(`${HOST}/v1`)
  })

  it('returns an empty string when there is no base url', () => {
    expect(buildMagpieImportDeeplink({ baseUrl: '', providerName: 'x', apiKey: 'k' })).toBe('')
  })
})

describe('limitMagpieModels', () => {
  it('caps the list and trims blanks', () => {
    const models = [' a ', '', 'b', '   ', ...Array.from({ length: 30 }, (_, i) => `m${i}`)]
    const capped = limitMagpieModels(models)
    expect(capped).toHaveLength(MAGPIE_MAX_MODELS)
    expect(capped[0]).toBe('a')
    expect(capped[1]).toBe('b')
  })

  it('returns an empty list for missing input', () => {
    expect(limitMagpieModels(undefined)).toEqual([])
  })
})

describe('buildMagpieBatchScript', () => {
  it('joins import commands so a whole group set lands in one paste', () => {
    const script = buildMagpieBatchScript([
      "magpie://import?name=A&chat=https://a/v1&key=k1",
      '',
      "magpie://import?name=B&chat=https://b/v1&key=k2"
    ])
    expect(script).toBe(
      "magpie import -y 'magpie://import?name=A&chat=https://a/v1&key=k1' && " +
        "magpie import -y 'magpie://import?name=B&chat=https://b/v1&key=k2'"
    )
  })

  it('escapes single quotes inside the link', () => {
    expect(buildMagpieBatchScript(["magpie://import?name=O'Brien"])).toBe(
      `magpie import -y 'magpie://import?name=O'\\''Brien'`
    )
  })

  it('returns an empty string when nothing is importable', () => {
    expect(buildMagpieBatchScript([])).toBe('')
    expect(buildMagpieBatchScript([''])).toBe('')
  })
})

describe('magpieProviderId', () => {
  it('uses a stable id derived from the group id', () => {
    expect(magpieProviderId(65)).toBe('sub2api-65')
    expect(magpieProviderId('66')).toBe('sub2api-66')
    expect(magpieProviderId(null)).toBe('')
  })
})
