import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { mkdtemp, rm } from 'node:fs/promises'
import http from 'node:http'
import os from 'node:os'
import path from 'node:path'
import test from 'node:test'
import { fileURLToPath } from 'node:url'

const claudeBin = '/Users/markdonish/.local/bin/claude'
const expectedClaudeVersion = '2.1.204 (Claude Code)'
const localAPIKey = 'sk-local-test'
const processTimeoutMs = 30_000
const repoRoot = fileURLToPath(new URL('../..', import.meta.url))

test('Claude Code local Messages model identity', async (t) => {
  const version = await runClaude(['--version'], {}, processTimeoutMs)
  assert.equal(version.code, 0, version.stderr)
  assert.equal(version.stdout.trim(), expectedClaudeVersion)

  const cases = [
    { name: 'native gpt-5.5', cliModel: 'gpt-5.5', expectedRequestModel: 'gpt-5.5' },
    { name: 'native gpt-5.6-sol', cliModel: 'gpt-5.6-sol', expectedRequestModel: 'gpt-5.6-sol' },
    { name: 'native gpt-5.6-terra', cliModel: 'gpt-5.6-terra', expectedRequestModel: 'gpt-5.6-terra' },
    { name: 'native gpt-5.6-luna', cliModel: 'gpt-5.6-luna', expectedRequestModel: 'gpt-5.6-luna' },
    {
      name: 'sonnet role alias resolves from environment',
      cliModel: 'sonnet',
      expectedRequestModel: 'gpt-5.4',
      env: { ANTHROPIC_DEFAULT_SONNET_MODEL: 'gpt-5.4' },
    },
    {
      name: 'legacy Claude alias stays visible',
      cliModel: 'claude-sonnet-4-6',
      expectedRequestModel: 'claude-sonnet-4-6',
    },
  ]

  for (const modelCase of cases) {
    await t.test(modelCase.name, async () => {
      const run = await runClaudeAgainstMock(modelCase)
      assert.equal(run.requests.length, 1, `expected one /v1/messages request, got ${run.requests.length}`)
      assert.equal(run.requests[0].model, modelCase.expectedRequestModel)
      assert.equal(run.result.type, 'result')
      assert.equal(run.result.subtype, 'success')
      assert.equal(run.result.is_error, false)
      assert.match(run.result.result, new RegExp(`local-model:${escapeRegExp(modelCase.expectedRequestModel)}`))
      assertModelUsageIdentity(run.result.modelUsage, modelCase.expectedRequestModel)
    })
  }

  await t.test('read-only tool round trip keeps model identity', async () => {
    const modelCase = {
      name: 'read-only tool',
      cliModel: 'gpt-5.6-terra',
      expectedRequestModel: 'gpt-5.6-terra',
      toolRoundTrip: true,
    }
    const run = await runClaudeAgainstMock(modelCase)
    assert.equal(
      run.requests.length,
      2,
      `expected tool request plus tool_result request, got ${run.requests.length}: ${JSON.stringify(summarizeRequests(run.requests))}`,
    )
    assert.deepEqual(run.requests.map((request) => request.model), ['gpt-5.6-terra', 'gpt-5.6-terra'])
    assert.deepEqual(run.countTokenRequests.map((request) => request.model), ['gpt-5.6-terra'])
    const assistantToolUse = run.requests[1].messages
      .find((message) => message.role === 'assistant')
      ?.content.find((part) => part.type === 'tool_use')
    const userToolResult = run.requests[1].messages
      .find((message) => message.role === 'user' && Array.isArray(message.content) && message.content.some((part) => part.type === 'tool_result'))
      ?.content.find((part) => part.type === 'tool_result')
    assert.equal(assistantToolUse?.id, 'toolu_local_read')
    assert.equal(userToolResult?.tool_use_id, assistantToolUse.id)
    assert.equal(run.result.subtype, 'success')
    assert.match(run.result.result, /local-tool-roundtrip:gpt-5\.6-terra/)
    assertModelUsageIdentity(run.result.modelUsage, 'gpt-5.6-terra')
  })
})

async function runClaudeAgainstMock(modelCase) {
  const requests = []
  const countTokenRequests = []
  const server = http.createServer(async (req, res) => {
    try {
      const requestPath = new URL(req.url ?? '/', 'http://127.0.0.1').pathname
      if (req.method !== 'POST' || (requestPath !== '/v1/messages' && requestPath !== '/v1/messages/count_tokens')) {
        res.writeHead(404, { 'content-type': 'application/json' })
        res.end(JSON.stringify({ error: { type: 'not_found_error', message: 'local mock only serves /v1/messages' } }))
        return
      }
      assert.equal(req.headers['x-api-key'], localAPIKey)
      const body = JSON.parse(await readBody(req))
      if (requestPath === '/v1/messages/count_tokens') {
        countTokenRequests.push(body)
        res.writeHead(200, { 'content-type': 'application/json' })
        res.end(JSON.stringify({ input_tokens: 2048 }))
        return
      }
      requests.push(body)

      if (modelCase.toolRoundTrip && requests.length === 1) {
        writeAnthropicToolUse(res, body.model, path.join(repoRoot, 'README.md'))
        return
      }
      const text = modelCase.toolRoundTrip
        ? `local-tool-roundtrip:${body.model}`
        : `local-model:${body.model}`
      writeAnthropicText(res, body.model, text)
    } catch (error) {
      res.writeHead(500, { 'content-type': 'application/json' })
      res.end(JSON.stringify({ error: { type: 'local_mock_error', message: String(error) } }))
    }
  })

  let tempHome
  try {
    await listenLocal(server)
    const address = server.address()
    assert(address && typeof address === 'object')
    assert.equal(address.address, '127.0.0.1')
    tempHome = await mkdtemp(path.join(os.tmpdir(), 'hfc-claude-identity-'))

    const args = [
      '--print',
      modelCase.toolRoundTrip ? 'Read README.md once, then answer with the mock result.' : 'Reply using the local mock.',
      '--output-format',
      'json',
      '--no-session-persistence',
      '--prompt-suggestions',
      'false',
      '--bare',
      '--safe-mode',
      '--disable-slash-commands',
      '--strict-mcp-config',
      '--mcp-config',
      '{"mcpServers":{}}',
      '--permission-mode',
      'dontAsk',
      '--tools',
      modelCase.toolRoundTrip ? 'Read' : '',
      '--model',
      modelCase.cliModel,
    ]
    if (modelCase.toolRoundTrip) {
      args.push('--allowedTools', 'Read')
    }

    const child = await runClaude(
      args,
      {
        HOME: tempHome,
        CLAUDE_CONFIG_DIR: path.join(tempHome, '.claude'),
        ANTHROPIC_BASE_URL: `http://127.0.0.1:${address.port}`,
        ANTHROPIC_API_KEY: localAPIKey,
        CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC: '1',
        DISABLE_TELEMETRY: '1',
        DISABLE_ERROR_REPORTING: '1',
        NO_COLOR: '1',
        CI: '1',
        TERM: 'dumb',
        LANG: 'C.UTF-8',
        PATH: '/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin',
        TMPDIR: os.tmpdir(),
        ...modelCase.env,
      },
      processTimeoutMs,
      repoRoot,
    )
    assert.equal(child.timedOut, false, `${modelCase.name} timed out after ${processTimeoutMs}ms`)
    assert.equal(child.code, 0, `${modelCase.name} failed\nstdout: ${child.stdout}\nstderr: ${child.stderr}`)
    const result = JSON.parse(child.stdout)
    return { requests, countTokenRequests, result }
  } finally {
    server.closeAllConnections?.()
    await new Promise((resolve) => server.close(resolve))
    if (tempHome) {
      await rm(tempHome, { recursive: true, force: true })
    }
  }
}

function runClaude(args, env, timeoutMs, cwd = repoRoot) {
  return new Promise((resolve, reject) => {
    const child = spawn(claudeBin, args, {
      cwd,
      env,
      stdio: ['ignore', 'pipe', 'pipe'],
    })
    let stdout = ''
    let stderr = ''
    let timedOut = false
    child.stdout.setEncoding('utf8')
    child.stderr.setEncoding('utf8')
    child.stdout.on('data', (chunk) => { stdout += chunk })
    child.stderr.on('data', (chunk) => { stderr += chunk })

    const timer = setTimeout(() => {
      timedOut = true
      child.kill('SIGKILL')
    }, timeoutMs)
    child.once('error', (error) => {
      clearTimeout(timer)
      reject(error)
    })
    child.once('close', (code, signal) => {
      clearTimeout(timer)
      if (child.exitCode === null && !child.killed) {
        child.kill('SIGKILL')
      }
      resolve({ code, signal, stdout, stderr, timedOut })
    })
  })
}

function listenLocal(server) {
  return new Promise((resolve, reject) => {
    server.once('error', reject)
    server.listen(0, '127.0.0.1', resolve)
  })
}

function readBody(req) {
  return new Promise((resolve, reject) => {
    const chunks = []
    let size = 0
    req.on('data', (chunk) => {
      size += chunk.length
      if (size > 4 * 1024 * 1024) {
        reject(new Error('local mock request exceeded 4 MiB'))
        req.destroy()
        return
      }
      chunks.push(chunk)
    })
    req.once('end', () => resolve(Buffer.concat(chunks).toString('utf8')))
    req.once('error', reject)
  })
}

function writeAnthropicText(res, model, text) {
  writeSSE(res, [
    ['message_start', {
      type: 'message_start',
      message: {
        id: 'msg_local_text', type: 'message', role: 'assistant', model,
        content: [], stop_reason: null, stop_sequence: null,
        usage: { input_tokens: 5, output_tokens: 0 },
      },
    }],
    ['content_block_start', { type: 'content_block_start', index: 0, content_block: { type: 'text', text: '' } }],
    ['content_block_delta', { type: 'content_block_delta', index: 0, delta: { type: 'text_delta', text } }],
    ['content_block_stop', { type: 'content_block_stop', index: 0 }],
    ['message_delta', { type: 'message_delta', delta: { stop_reason: 'end_turn', stop_sequence: null }, usage: { output_tokens: 4 } }],
    ['message_stop', { type: 'message_stop' }],
  ])
}

function writeAnthropicToolUse(res, model, filePath) {
  writeSSE(res, [
    ['message_start', {
      type: 'message_start',
      message: {
        id: 'msg_local_tool', type: 'message', role: 'assistant', model,
        content: [], stop_reason: null, stop_sequence: null,
        usage: { input_tokens: 5, output_tokens: 0 },
      },
    }],
    ['content_block_start', {
      type: 'content_block_start', index: 0,
      content_block: { type: 'tool_use', id: 'toolu_local_read', name: 'Read', input: {} },
    }],
    ['content_block_delta', {
      type: 'content_block_delta', index: 0,
      delta: { type: 'input_json_delta', partial_json: JSON.stringify({ file_path: filePath }) },
    }],
    ['content_block_stop', { type: 'content_block_stop', index: 0 }],
    ['message_delta', { type: 'message_delta', delta: { stop_reason: 'tool_use', stop_sequence: null }, usage: { output_tokens: 6 } }],
    ['message_stop', { type: 'message_stop' }],
  ])
}

function writeSSE(res, events) {
  res.writeHead(200, {
    'content-type': 'text/event-stream',
    'cache-control': 'no-cache',
    connection: 'close',
  })
  for (const [event, data] of events) {
    res.write(`event: ${event}\ndata: ${JSON.stringify(data)}\n\n`)
  }
  res.end()
}

function assertModelUsageIdentity(modelUsage, expectedModel) {
  if (modelUsage === undefined || modelUsage === null) {
    return
  }
  assert.equal(typeof modelUsage, 'object')
  assert.deepEqual(Object.keys(modelUsage), [expectedModel])
}

function summarizeRequests(requests) {
  return requests.map((request) => ({
    model: request.model,
    messages: (request.messages ?? []).map((message) => ({
      role: message.role,
      content: Array.isArray(message.content)
        ? message.content.map((part) => ({
          type: part.type,
          name: part.name,
          tool_use_id: part.tool_use_id,
          textLength: typeof part.text === 'string' ? part.text.length : undefined,
        }))
        : { type: typeof message.content, length: typeof message.content === 'string' ? message.content.length : undefined },
    })),
  }))
}

function escapeRegExp(value) {
  return value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
}
