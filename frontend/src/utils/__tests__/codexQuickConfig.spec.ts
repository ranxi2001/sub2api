import { spawnSync } from 'node:child_process'
import { mkdtempSync, mkdirSync, readFileSync, readdirSync, realpathSync, rmSync, statSync, writeFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { afterEach, describe, expect, it } from 'vitest'
import { Language, Parser } from 'web-tree-sitter'
import {
  buildCodexQuickConfigToml,
  buildMacLinuxCodexQuickConfigScript,
  buildWindowsCmdCodexQuickConfigScript,
  encodeUtf8Base64,
  isCodexQuickConfigPlatform,
  normalizeCodexBaseUrl
} from '@/utils/codexQuickConfig'

function decodeBase64Utf8(value: string): string {
  const bytes = atob(value).split('').map((character) => character.charCodeAt(0))
  return new TextDecoder().decode(new Uint8Array(bytes))
}

const workspaces: string[] = []
const input = { apiKey: 'sk-secret-value', baseUrl: 'https://example.com', platform: 'openai' as const }
const nativeUnix = process.platform !== 'win32'
const powershell = process.env.SUB2API_TEST_POWERSHELL || (process.platform === 'win32' ? 'powershell.exe' : '')
const require = createRequire(import.meta.url)
let batchParserPromise: Promise<Parser> | null = null

async function getBatchParser() {
  if (!batchParserPromise) {
    batchParserPromise = (async () => {
      await Parser.init({ locateFile: () => require.resolve('web-tree-sitter/tree-sitter.wasm') })
      const language = await Language.load(require.resolve('tree-sitter-batch/tree-sitter-batch.wasm'))
      return new Parser().setLanguage(language)
    })()
  }
  return batchParserPromise
}

function workspace() {
  const dir = mkdtempSync(join(tmpdir(), 'codex-quick-config-'))
  workspaces.push(dir)
  return dir
}

function unixRunner(options: { os?: string; dialogExit?: number; codeHome?: string } = {}) {
  const dir = workspace()
  const bin = join(dir, 'bin')
  mkdirSync(bin)
  writeFileSync(join(bin, 'uname'), `#!/bin/bash\nprintf '%s\\n' '${options.os || 'Linux'}'\n`, { mode: 0o755 })
  writeFileSync(join(bin, 'osascript'), `#!/bin/bash\nprintf '%s\\n' "$@" > "$HOME/dialog.log"\n/bin/cat > "$HOME/dialog.applescript"\nexit ${options.dialogExit || 0}\n`, { mode: 0o755 })
  const env = { ...process.env, HOME: dir, CODEX_HOME: options.codeHome || '', PATH: `${bin}:/usr/bin:/bin` }
  return {
    dir,
    bin,
    run: (script: string) => spawnSync('/bin/bash', ['-s'], { input: script, encoding: 'utf8', env, cwd: dir })
  }
}

function psRunner(codeHome?: string, useCmd = false, environment: NodeJS.ProcessEnv = {}) {
  const dir = workspace()
  const configDir = codeHome || join(dir, '中文 空格 & ! % 目录')
  return {
    dir,
    configDir,
    run: (script: string) => {
      if (useCmd) {
        const file = join(dir, '中文 空格 & (setup) !.cmd')
        // Keep the generated bootstrap intact; only suppress its blocking dialog.
        writeFileSync(file, script.replace('$shell = New-Object -ComObject WScript.Shell', "throw 'Dialog unavailable in test'"))
        return spawnSync(process.env.SUB2API_TEST_CMD || process.env.ComSpec || 'cmd.exe', ['/d', '/v:off', '/s', '/c', `""${file}""`], {
          encoding: 'utf8', env: { ...process.env, CODEX_HOME: configDir, ...environment },
          windowsVerbatimArguments: true, input: '\r\n', timeout: 20000
        })
      }
      const ps = script.slice(script.lastIndexOf('# SUB2API_POWERSHELL') + '# SUB2API_POWERSHELL'.length)
      // Isolate system dialogs so Windows test runs never wait for a user click.
      const testScript = `function New-Object { throw 'Dialog unavailable in test' }\n${ps}`
      const file = join(dir, 'configure.ps1')
      writeFileSync(file, `\uFEFF${testScript}`)
      return spawnSync(powershell, ['-NoLogo', '-NoProfile', '-NonInteractive', '-ExecutionPolicy', 'Bypass', '-File', file], {
        encoding: 'utf8', env: { ...process.env, CODEX_HOME: configDir, ...environment }, timeout: 20000
      })
    }
  }
}

afterEach(() => {
  for (const dir of workspaces.splice(0)) rmSync(dir, { recursive: true, force: true })
})

describe('codexQuickConfig', () => {
  it('only accepts platforms served through a Codex-compatible API', () => {
    expect(isCodexQuickConfigPlatform('openai')).toBe(true)
    expect(isCodexQuickConfigPlatform('composite')).toBe(true)
    expect(isCodexQuickConfigPlatform('typesafe')).toBe(false)
    expect(isCodexQuickConfigPlatform(null)).toBe(false)
  })

  it('normalizes the API base and escapes TOML values', () => {
    expect(normalizeCodexBaseUrl(' https://example.com/// ')).toBe('https://example.com/v1')
    expect(normalizeCodexBaseUrl('https://example.com/v1')).toBe('https://example.com/v1')

    const config = buildCodexQuickConfigToml({
      apiKey: 'sk-quick-"test"',
      baseUrl: 'https://example.com',
      platform: 'openai'
    })
    expect(config).toContain('experimental_bearer_token = "sk-quick-\\"test\\""')
    expect(config).toContain('base_url = "https://example.com/v1"')
    expect(config).not.toContain('model_catalog_json')
  })

  it('requests Fast at the TOML root only for OpenAI quick setup', () => {
    const config = buildCodexQuickConfigToml(input)
    expect(config.split('\n[')[0]).toContain('service_tier = "fast"')
    expect(config.match(/^service_tier = /gm)).toHaveLength(1)
    for (const platform of ['anthropic', 'grok', 'composite'] as const) {
      expect(buildCodexQuickConfigToml({ ...input, platform })).not.toContain('service_tier')
    }
  })

  it('embeds model catalog data only when requested', () => {
    const catalog = '{"models":[{"slug":"gpt-test"}]}'
    const config = buildCodexQuickConfigToml({
      apiKey: 'sk-catalog',
      baseUrl: 'https://example.com/v1',
      modelCatalogContent: catalog,
      modelCatalogPath: '~/.codex/codex-models.json'
    })
    expect(config).toContain('model_catalog_json = "~/.codex/codex-models.json"')

    const script = buildMacLinuxCodexQuickConfigScript({
      apiKey: 'sk-catalog',
      baseUrl: 'https://example.com',
      modelCatalogContent: catalog
    })
    const payload = script.match(/CATALOG_PAYLOAD='([^']+)'/)?.[1]
    expect(payload).toBeDefined()
    expect(decodeBase64Utf8(payload!)).toBe(catalog)
    expect(script).toContain('codex-models.json')
  })

  it('generates rerunnable Unix and Windows scripts without exposing raw key text', () => {
    const unixScript = buildMacLinuxCodexQuickConfigScript(input)
    const windowsScript = buildWindowsCmdCodexQuickConfigScript(input)

    expect(unixScript).toContain('#!/usr/bin/env bash')
    expect(unixScript).toContain('mv -f')
    expect(unixScript).toContain('Codex 配置成功。')
    expect(unixScript).toContain('请完全退出并重启 Codex 以加载新配置。')
    expect(unixScript).toContain('osascript - "$message" "$dialog_kind"')
    expect(windowsScript).toContain('@echo off')
    expect(windowsScript).toContain('System32\\WindowsPowerShell\\v1.0\\powershell.exe')
    expect(windowsScript).toContain('Sysnative\\WindowsPowerShell\\v1.0\\powershell.exe')
    expect(windowsScript).toContain('-NoLogo -NoProfile -NonInteractive -STA -ExecutionPolicy Bypass -EncodedCommand')
    expect(windowsScript).not.toContain(' -Command "')
    expect(windowsScript).toContain('Publish-File $configTemp $configFile')
    expect(windowsScript).toContain(encodeUtf8Base64('Codex 配置成功。'))
    expect(windowsScript).toContain(encodeUtf8Base64('请完全退出并重启 Codex 以加载新配置。'))
    expect(windowsScript).toContain('New-Object -ComObject WScript.Shell')
    expect(windowsScript).toContain('$shell.Popup(')
    expect(windowsScript).toContain('pause')
    expect(unixScript).not.toContain('sk-secret-value')
    expect(windowsScript).not.toContain('sk-secret-value')

    const payload = windowsScript.match(/\$configPayload = @\(\s*'([^']+)'/)?.[1]
    expect(payload).toBeDefined()
    expect(decodeBase64Utf8(payload!)).toContain('experimental_bearer_token')
    expect(decodeBase64Utf8(payload!).split('\n[')[0]).toContain('service_tier = "fast"')
    expect(encodeUtf8Base64('中文')).not.toBe('')
  })

  it('parses the generated CMD bootstrap with tree-sitter-batch', async () => {
    const parser = await getBatchParser()
    const script = buildWindowsCmdCodexQuickConfigScript({
      ...input,
      modelCatalogContent: JSON.stringify({ models: Array.from({ length: 2000 }, (_, i) => ({ slug: `模型-${i}` })) })
    })
    const markerIndex = script.indexOf('# SUB2API_POWERSHELL')
    expect(markerIndex).toBeGreaterThan(0)
    const bootstrap = script.slice(0, markerIndex)
    const tree = parser.parse(bootstrap)
    expect(tree).not.toBeNull()
    expect(tree!.rootNode.hasError, tree!.rootNode.toString()).toBe(false)
    tree!.delete()

    const invalidTree = parser.parse(bootstrap.replace('if not exist "%SUB2API_POWERSHELL%"', 'if not exist ('))
    expect(invalidTree).not.toBeNull()
    expect(invalidTree!.rootNode.hasError).toBe(true)
    invalidTree!.delete()
  })

  it('escapes all TOML control characters', () => {
    const config = buildCodexQuickConfigToml({ ...input, apiKey: 'sk-\t\n\r\x00\x7f' })
    expect(config).toContain('experimental_bearer_token = "sk-\\u0009\\u000a\\u000d\\u0000\\u007f"')
  })

  it('keeps large Windows payloads out of CMD commands and environment variables', () => {
    const script = buildWindowsCmdCodexQuickConfigScript({
      ...input,
      apiKey: 'sk-' + 'x'.repeat(20000),
      modelCatalogContent: JSON.stringify({ models: Array.from({ length: 2000 }, (_, i) => ({ slug: `模型-${i}` })) }),
      modelCatalogPath: '%USERPROFILE%\\.codex\\codex-models.json'
    })
    expect(script.length).toBeGreaterThan(8191)
    expect(script.split('\r\n').every((line) => line.length < 2500)).toBe(true)
    expect([...script].every((character) => character.charCodeAt(0) <= 0x7f)).toBe(true)
    expect(script.replace(/\r\n/g, '')).not.toContain('\n')
    expect(script).not.toContain('set "SUB2API_CODEX_PAYLOAD=')
    expect(script).not.toContain('%USERPROFILE%')
    expect(script).not.toMatch(/Add-Type|Import-Module|Install-Module|npm|python|pwsh/)
    expect(script).toContain('[IO.File]::WriteAllBytes')
    expect(script).toContain('endlocal & exit /b %SUB2API_CODEX_EXIT%')
  })

  it.runIf(nativeUnix)('executes and reruns with UTF-8 content, absolute catalog paths and private files', () => {
    const runner = unixRunner()
    const catalog = '{"models":[{"slug":"中文模型"}]}'
    const script = buildMacLinuxCodexQuickConfigScript({ ...input, modelCatalogContent: catalog })
    const configDir = join(runner.dir, '.codex')
    expect(runner.run(script).status).toBe(0)
    expect(runner.run(script).status).toBe(0)
    const config = readFileSync(join(configDir, 'config.toml'), 'utf8')
    expect(config).toContain(`model_catalog_json = "${realpathSync(configDir)}/codex-models.json"`)
    expect(config).toContain('experimental_bearer_token = "sk-secret-value"')
    expect(config.split('\n[')[0]).toContain('service_tier = "fast"')
    expect(readFileSync(join(configDir, 'codex-models.json'), 'utf8')).toBe(catalog)
    expect(statSync(join(configDir, 'config.toml')).mode & 0o777).toBe(0o600)
    expect(readdirSync(configDir).sort()).toEqual(['codex-models.json', 'config.toml'])
    expect(runner.run(buildMacLinuxCodexQuickConfigScript(input)).status).toBe(0)
    expect(readFileSync(join(configDir, 'config.toml'), 'utf8')).not.toContain('model_catalog_json')
  })

  it.runIf(nativeUnix)('resolves a custom relative CODEX_HOME and escapes path characters', () => {
    const relativeHome = '中文 空格 & ! " \\ \t\n目录'
    const runner = unixRunner({ codeHome: relativeHome })
    const result = runner.run(buildMacLinuxCodexQuickConfigScript({ ...input, modelCatalogContent: '{}' }))
    expect(result.status, result.stderr).toBe(0)
    const configDir = join(runner.dir, relativeHome)
    const config = readFileSync(join(configDir, 'config.toml'), 'utf8')
    const catalogPath = JSON.parse(config.split('\n')[0].slice('model_catalog_json = '.length))
    expect(catalogPath).toBe(realpathSync(join(configDir, 'codex-models.json')))
    expect(readFileSync(catalogPath, 'utf8')).toBe('{}')
  })

  it.runIf(nativeUnix)('reports success through the macOS system dialog with terminal fallback', () => {
    const runner = unixRunner({ os: 'Darwin', dialogExit: 1 })
    const result = runner.run(buildMacLinuxCodexQuickConfigScript(input))
    expect(result.status).toBe(0)
    expect(result.stdout).toContain('Codex 配置成功。')
    expect(readFileSync(join(runner.dir, 'dialog.log'), 'utf8')).toContain('\nsuccess\n')
    expect(readFileSync(join(runner.dir, 'dialog.applescript'), 'utf8')).toContain('with icon note')
  })

  it.runIf(process.platform === 'darwin')('compiles its dialog with the native AppleScript compiler', () => {
    const runner = unixRunner({ os: 'Darwin' })
    expect(runner.run(buildMacLinuxCodexQuickConfigScript(input)).status).toBe(0)
    const result = spawnSync('/usr/bin/osacompile', ['-o', join(runner.dir, 'dialog.scpt'), '-'], {
      input: readFileSync(join(runner.dir, 'dialog.applescript'), 'utf8'), encoding: 'utf8'
    })
    expect(result.status, result.stderr).toBe(0)
  })

  it.runIf(nativeUnix)('reports an early filesystem error through the macOS system dialog', () => {
    const runner = unixRunner({ os: 'Darwin', codeHome: 'not-a-directory' })
    writeFileSync(join(runner.dir, 'not-a-directory'), 'unchanged')
    const result = runner.run(buildMacLinuxCodexQuickConfigScript(input))
    expect(result.status).not.toBe(0)
    expect(result.stdout).toContain('Codex 配置失败。')
    expect(readFileSync(join(runner.dir, 'dialog.log'), 'utf8')).toContain('\nerror\n')
    expect(readFileSync(join(runner.dir, 'not-a-directory'), 'utf8')).toBe('unchanged')
  })

  it.runIf(nativeUnix)('retains the old config and cleans staged files when decoding fails', () => {
    const runner = unixRunner()
    const configDir = join(runner.dir, '.codex')
    mkdirSync(configDir)
    writeFileSync(join(configDir, 'config.toml'), 'original config')
    const script = buildMacLinuxCodexQuickConfigScript({ ...input, modelCatalogContent: '{}' })
      .replace(/CATALOG_PAYLOAD='[^']*'/, "CATALOG_PAYLOAD='@@@'")
    const result = runner.run(script)
    expect(result.status).not.toBe(0)
    expect(result.stdout).toContain('Codex 配置失败。')
    expect(readFileSync(join(configDir, 'config.toml'), 'utf8')).toBe('original config')
    expect(readdirSync(configDir)).toEqual(['config.toml'])
  })

  it.runIf(nativeUnix)('never reports success when the config or catalog target is a directory', () => {
    for (const target of ['config.toml', 'codex-models.json']) {
      const runner = unixRunner()
      const configDir = join(runner.dir, '.codex')
      mkdirSync(join(configDir, target), { recursive: true })
      if (target !== 'config.toml') writeFileSync(join(configDir, 'config.toml'), 'original config')
      const result = runner.run(buildMacLinuxCodexQuickConfigScript({ ...input, modelCatalogContent: '{}' }))
      expect(result.status).toBe(1)
      expect(result.stdout).toContain('Codex 配置失败。')
      expect(readdirSync(join(configDir, target))).toEqual([])
      expect(readdirSync(configDir).some((name) => name.includes('.tmp.'))).toBe(false)
      if (target !== 'config.toml') expect(readFileSync(join(configDir, 'config.toml'), 'utf8')).toBe('original config')
    }
  })

  it.runIf(nativeUnix)('supports BSD base64 -D when --decode is unavailable', () => {
    const runner = unixRunner()
    writeFileSync(join(runner.bin, 'base64'), '#!/bin/bash\nif [ "$1" != "-D" ]; then exit 1; fi\nexec /usr/bin/base64 -d\n', { mode: 0o755 })
    const result = runner.run(buildMacLinuxCodexQuickConfigScript(input))
    expect(result.status, result.stderr).toBe(0)
    expect(readFileSync(join(runner.dir, '.codex/config.toml'), 'utf8')).toContain('sk-secret-value')
  })

  it.runIf(nativeUnix)('reports a missing decoder without replacing the old config', () => {
    const runner = unixRunner()
    writeFileSync(join(runner.bin, 'base64'), '#!/bin/bash\nexit 1\n', { mode: 0o755 })
    const result = runner.run(buildMacLinuxCodexQuickConfigScript(input))
    expect(result.status).toBe(1)
    expect(result.stderr).toContain('系统 base64 组件不可用。')
    expect(result.stdout).toContain('Codex 配置失败。')
    expect(readdirSync(runner.dir)).not.toContain('.codex')
  })

  it.runIf(Boolean(powershell))('executes the Windows payload and atomically replaces existing files on rerun', () => {
    const runner = psRunner()
    const catalog = JSON.stringify({ models: Array.from({ length: 2000 }, (_, i) => ({ slug: `中文-${i}` })) })
    const script = buildWindowsCmdCodexQuickConfigScript({ ...input, apiKey: 'sk-' + 'x'.repeat(20000), modelCatalogContent: catalog })
    for (let i = 0; i < 2; i++) {
      const result = runner.run(script)
      expect(result.status, result.stderr).toBe(0)
      expect(result.stdout).toContain('Codex 配置成功。')
    }
    const configBytes = readFileSync(join(runner.configDir, 'config.toml'))
    expect(configBytes.subarray(0, 3).toString('hex')).not.toBe('efbbbf')
    const config = configBytes.toString('utf8')
    const catalogPath = JSON.parse(config.split(/\r?\n/)[0].slice('model_catalog_json = '.length))
    expect(catalogPath).toBe(join(runner.configDir, 'codex-models.json'))
    expect(readFileSync(catalogPath, 'utf8')).toBe(catalog)
    expect(readdirSync(runner.configDir).sort()).toEqual(['codex-models.json', 'config.toml'])
  }, 60000)

  it.runIf(process.platform === 'win32')('runs the downloaded CMD against existing config and catalog files', () => {
    const runner = psRunner(undefined, true)
    mkdirSync(runner.configDir, { recursive: true })
    writeFileSync(join(runner.configDir, 'config.toml'), 'original config')
    writeFileSync(join(runner.configDir, 'codex-models.json'), '{"models":[]}')
    const catalog = JSON.stringify({ models: Array.from({ length: 2000 }, (_, i) => ({ slug: `中文-${i}` })) })
    const script = buildWindowsCmdCodexQuickConfigScript({ ...input, modelCatalogContent: catalog })
    for (let i = 0; i < 2; i++) {
      const result = runner.run(script)
      expect(result.error).toBeUndefined()
      expect(result.status, result.stdout + result.stderr).toBe(0)
      expect(readFileSync(join(runner.configDir, 'config.toml'), 'utf8')).toContain('experimental_bearer_token = "sk-secret-value"')
      expect(readFileSync(join(runner.configDir, 'codex-models.json'), 'utf8')).toBe(catalog)
      expect(readdirSync(runner.configDir).sort()).toEqual(['codex-models.json', 'config.toml'])
    }
  }, 60000)

  it.runIf(process.platform === 'win32')('creates the config through CMD without PATH dependencies or inherited exit codes', () => {
    const runner = psRunner(undefined, true, { PATH: '', Path: '', ERRORLEVEL: '1' })
    const result = runner.run(buildWindowsCmdCodexQuickConfigScript(input))
    expect(result.error).toBeUndefined()
    expect(result.status, result.stdout + result.stderr).toBe(0)
    expect(readFileSync(join(runner.configDir, 'config.toml'), 'utf8')).toContain('experimental_bearer_token = "sk-secret-value"')
    expect(readdirSync(runner.configDir)).toEqual(['config.toml'])
  }, 30000)

  it.runIf(process.platform === 'win32')('propagates a CMD failure exit code and keeps the existing config', () => {
    const runner = psRunner(undefined, true, { ERRORLEVEL: '0' })
    mkdirSync(join(runner.configDir, 'codex-models.json'), { recursive: true })
    writeFileSync(join(runner.configDir, 'config.toml'), 'original config')
    const result = runner.run(buildWindowsCmdCodexQuickConfigScript({ ...input, modelCatalogContent: '{}' }))
    expect(result.error).toBeUndefined()
    expect(result.status, result.stdout + result.stderr).toBe(1)
    expect(readFileSync(join(runner.configDir, 'config.toml'), 'utf8')).toBe('original config')
    expect(readdirSync(runner.configDir).sort()).toEqual(['codex-models.json', 'config.toml'])
  }, 30000)

  it.runIf(Boolean(powershell))('reports Windows write failures without leftover temporary files', () => {
    const runner = psRunner()
    mkdirSync(runner.configDir, { recursive: true })
    mkdirSync(join(runner.configDir, 'config.toml'))
    const result = runner.run(buildWindowsCmdCodexQuickConfigScript(input))
    expect(result.status).toBe(1)
    expect(result.stdout).toContain('Codex 配置失败。')
    expect(readdirSync(runner.configDir)).toEqual(['config.toml'])
  }, 30000)

  it.runIf(Boolean(powershell))('restores the original catalog when the config cannot be published', () => {
    const runner = psRunner()
    mkdirSync(join(runner.configDir, 'config.toml'), { recursive: true })
    const originalCatalog = '{"models":[{"slug":"original-model"}]}'
    writeFileSync(join(runner.configDir, 'codex-models.json'), originalCatalog)
    const result = runner.run(buildWindowsCmdCodexQuickConfigScript({ ...input, modelCatalogContent: '{"models":[]}' }))
    expect(result.status, result.stdout + result.stderr).toBe(1)
    expect(result.stdout).toContain('Codex 配置失败。')
    expect(readFileSync(join(runner.configDir, 'codex-models.json'), 'utf8')).toBe(originalCatalog)
    expect(readdirSync(runner.configDir).sort()).toEqual(['codex-models.json', 'config.toml'])
  }, 30000)

  it.runIf(Boolean(powershell))('removes a newly published catalog when the config cannot be published', () => {
    const runner = psRunner()
    mkdirSync(join(runner.configDir, 'config.toml'), { recursive: true })
    const result = runner.run(buildWindowsCmdCodexQuickConfigScript({ ...input, modelCatalogContent: '{}' }))
    expect(result.status).toBe(1)
    expect(readdirSync(runner.configDir)).toEqual(['config.toml'])
  }, 30000)

  it.runIf(Boolean(powershell))('keeps a recoverable backup and reports its path if rollback also fails', () => {
    const runner = psRunner()
    mkdirSync(join(runner.configDir, 'config.toml'), { recursive: true })
    const originalCatalog = '{"models":[{"slug":"original-model"}]}'
    writeFileSync(join(runner.configDir, 'codex-models.json'), originalCatalog)
    // Simulate a second filesystem failure specifically during rollback.
    const script = buildWindowsCmdCodexQuickConfigScript({ ...input, modelCatalogContent: '{}' })
      .replace('Publish-File $catalogBackup $catalogFile', "throw 'Catalog is locked during rollback'")
    const result = runner.run(script)
    expect(result.status).toBe(1)
    const backup = readdirSync(runner.configDir).find((name) => name.startsWith('codex-models.json.backup.'))
    expect(backup).toBeDefined()
    expect(readFileSync(join(runner.configDir, backup!), 'utf8')).toBe(originalCatalog)
    expect(result.stdout).toContain(join(runner.configDir, backup!))
    expect(readdirSync(runner.configDir).some((name) => name.includes('.tmp.'))).toBe(false)
  }, 30000)

  it.runIf(Boolean(powershell))('cleans Windows staging files and preserves the config on catalog decode failure', () => {
    const runner = psRunner()
    mkdirSync(runner.configDir, { recursive: true })
    writeFileSync(join(runner.configDir, 'config.toml'), 'original config')
    const script = buildWindowsCmdCodexQuickConfigScript({ ...input, modelCatalogContent: '{}' })
      .replace(/(\$catalogPayload = @\(\s*)'[^']*'/, "$1'@@@'")
    const result = runner.run(script)
    expect(result.status).toBe(1)
    expect(readFileSync(join(runner.configDir, 'config.toml'), 'utf8')).toBe('original config')
    expect(readdirSync(runner.configDir)).toEqual(['config.toml'])
  }, 30000)
})
