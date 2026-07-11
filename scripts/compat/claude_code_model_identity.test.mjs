import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { mkdtemp, readFile, rm, writeFile } from 'node:fs/promises'
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
  await assertNativeGPTTemplateMatchesUI()

  const cases = [
    { name: 'native gpt-5.5', cliModel: 'gpt-5.5', expectedRequestModel: 'gpt-5.5' },
    { name: 'native gpt-5.6-sol', cliModel: 'gpt-5.6-sol', expectedRequestModel: 'gpt-5.6-sol' },
    { name: 'native gpt-5.6-terra', cliModel: 'gpt-5.6-terra', expectedRequestModel: 'gpt-5.6-terra' },
    { name: 'native gpt-5.6-luna', cliModel: 'gpt-5.6-luna', expectedRequestModel: 'gpt-5.6-luna' },
    {
      name: 'opus role alias resolves from template environment',
      cliModel: 'opus',
      expectedRequestModel: 'gpt-5.6-sol',
      authMode: 'token',
      useBare: false,
      isolatedCwd: true,
      env: nativeGPTTemplateEnv(),
    },
    {
      name: 'sonnet role alias resolves from environment',
      cliModel: 'sonnet',
      expectedRequestModel: 'gpt-5.6-terra',
      authMode: 'token',
      useBare: false,
      isolatedCwd: true,
      env: nativeGPTTemplateEnv(),
    },
    {
      name: 'haiku role alias resolves from template environment',
      cliModel: 'haiku',
      expectedRequestModel: 'gpt-5.6-luna',
      authMode: 'token',
      useBare: false,
      isolatedCwd: true,
      env: nativeGPTTemplateEnv(),
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
      if (modelCase.authMode === 'token') {
        assertLocalBearerRequests(run.requestMetadata)
      }
    })
  }

  await t.test('exact UI template uses bearer auth and ANTHROPIC_MODEL without --model', async () => {
    const modelCase = {
      name: 'exact UI template environment',
      expectedRequestModel: 'gpt-5.6-terra',
      authMode: 'token',
      useBare: false,
      isolatedCwd: true,
      env: nativeGPTTemplateEnv(),
    }
    const run = await runClaudeAgainstMock(modelCase)
    assert.equal(run.requests.length, 1, `expected one /v1/messages request, got ${run.requests.length}`)
    assert.equal(run.invocationArgs.includes('--model'), false, 'exact UI template case must not pass --model')
    assert.equal(run.invocationArgs.includes('--safe-mode'), true, 'exact UI template case must use safe-mode')
    assert.equal(run.requests[0].model, 'gpt-5.6-terra')
    assertLocalBearerRequests(run.requestMetadata)
    assert.equal(run.result.type, 'result')
    assert.equal(run.result.subtype, 'success')
    assert.equal(run.result.is_error, false)
    assert.match(run.result.result, /local-model:gpt-5\.6-terra/)
    assertModelUsageIdentity(run.result.modelUsage, 'gpt-5.6-terra', true)
  })

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

  await t.test('CLAUDE_CODE_SUBAGENT_MODEL=inherit keeps a custom read-only agent on gpt-5.6-terra', async () => {
    const modelCase = {
      name: 'custom subagent inheritance',
      expectedRequestModel: 'gpt-5.6-terra',
      authMode: 'token',
      useBare: false,
      useSafeMode: false,
      isolatedCwd: true,
      agentRoundTrip: true,
      env: nativeGPTTemplateEnv(),
    }
    const run = await runClaudeAgainstMock(modelCase)
    assert.ok(
      run.agentToolSchema,
      `Claude Code did not advertise an Agent/Task tool schema: ${JSON.stringify(summarizeRequests(run.requests))}`,
    )
    assert.match(run.agentToolSchema.name, /^(Agent|Task)$/)
    assert.equal(run.invocationArgs.includes('--model'), false, 'subagent inheritance case must not pass --model')
    assert.equal(run.requests.length, 5, `expected async main/agent request round trip, got ${run.requests.length}: ${JSON.stringify(summarizeRequests(run.requests))}`)
    assert.deepEqual(run.requests.map((request) => request.model), Array(5).fill('gpt-5.6-terra'))
    assertLocalBearerRequests(run.requestMetadata)

    const childToolResultRequest = run.requests.find((request) => findToolResult(request, 'toolu_local_subagent_read'))
    const mainToolResultRequest = run.requests.find((request) => findToolResult(request, 'toolu_local_agent'))
    const completionNotificationRequest = run.requests.find((request) => requestContainsText(request, '<task-notification>'))
    const mainAgentToolUse = run.requests.map((request) => findToolUse(request, 'toolu_local_agent')).find(Boolean)
    const childReadToolUse = run.requests.map((request) => findToolUse(request, 'toolu_local_subagent_read')).find(Boolean)
    assert.equal(mainAgentToolUse?.name, run.agentToolSchema.name)
    assert.equal(mainAgentToolUse?.input?.subagent_type, 'local-reader')
    assert.equal(mainAgentToolUse?.input?.model, undefined, 'Agent/Task tool must not override the inherited model')
    assert.equal(childReadToolUse?.name, 'Read')
    assert.equal(path.basename(childReadToolUse?.input?.file_path ?? ''), 'local-read-fixture.txt')
    assert.ok(childToolResultRequest, 'custom subagent did not return the Read tool_result')
    assert.ok(mainToolResultRequest, 'main agent did not receive the custom Agent/Task tool_result')
    assert.ok(completionNotificationRequest, 'main agent did not receive the custom subagent completion notification')
    assert.ok(
      requestContainsText(completionNotificationRequest, 'subagent-read-complete:gpt-5.6-terra'),
      'custom subagent completion notification did not carry the mocked read result',
    )
    assert.equal(run.result.subtype, 'success')
    assert.match(run.result.result, /local-subagent-launched:gpt-5\.6-terra/)
    assertModelUsageIdentity(run.result.modelUsage, 'gpt-5.6-terra', true)
  })
})

async function runClaudeAgainstMock(modelCase) {
  const requests = []
  const countTokenRequests = []
  const requestMetadata = []
  let agentToolSchema
  let childAgentCompleted = false
  const server = http.createServer(async (req, res) => {
    try {
      const requestPath = new URL(req.url ?? '/', 'http://127.0.0.1').pathname
      if (req.method !== 'POST' || (requestPath !== '/v1/messages' && requestPath !== '/v1/messages/count_tokens')) {
        res.writeHead(404, { 'content-type': 'application/json' })
        res.end(JSON.stringify({ error: { type: 'not_found_error', message: 'local mock only serves /v1/messages' } }))
        return
      }
      requestMetadata.push({
        authorization: req.headers.authorization,
        host: req.headers.host,
        remoteAddress: req.socket.remoteAddress,
        xApiKey: req.headers['x-api-key'],
      })
      if (modelCase.authMode === 'token') {
        assert.equal(req.headers.authorization, `Bearer ${localAPIKey}`)
        assert.equal(req.headers['x-api-key'], undefined)
      } else {
        assert.equal(req.headers['x-api-key'], localAPIKey)
      }
      const body = JSON.parse(await readBody(req))
      if (requestPath === '/v1/messages/count_tokens') {
        countTokenRequests.push(body)
        res.writeHead(200, { 'content-type': 'application/json' })
        res.end(JSON.stringify({ input_tokens: 2048 }))
        return
      }
      requests.push(body)

      if (modelCase.agentRoundTrip) {
        if (findToolResult(body, 'toolu_local_agent')) {
          writeAnthropicText(res, body.model, `local-subagent-launched:${body.model}`)
          return
        }
        if (findToolResult(body, 'toolu_local_subagent_read')) {
          childAgentCompleted = true
          writeAnthropicText(res, body.model, `subagent-read-complete:${body.model}`)
          return
        }
        if (childAgentCompleted && requestContainsText(body, '<task-notification>')) {
          writeAnthropicText(res, body.model, `local-subagent-roundtrip:${body.model}`)
          return
        }
        const advertisedAgentTool = (body.tools ?? []).find((tool) => tool.name === 'Agent' || tool.name === 'Task')
        if (advertisedAgentTool) {
          agentToolSchema = advertisedAgentTool
          writeAnthropicGenericToolUse(
            res,
            body.model,
            'toolu_local_agent',
            advertisedAgentTool.name,
            buildAgentToolInput(advertisedAgentTool),
          )
          return
        }
        const advertisedReadTool = (body.tools ?? []).find((tool) => tool.name === 'Read')
        assert.ok(advertisedReadTool, `custom subagent request did not advertise Read: ${JSON.stringify((body.tools ?? []).map((tool) => tool.name))}`)
        writeAnthropicGenericToolUse(
          res,
          body.model,
          'toolu_local_subagent_read',
          'Read',
          { file_path: path.join(tempHome, 'local-read-fixture.txt') },
        )
        return
      }

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
    await writeFile(path.join(tempHome, 'local-read-fixture.txt'), 'local read-only subagent fixture\n', { mode: 0o600 })

    const args = [
      '--print',
      modelCase.agentRoundTrip
        ? 'Use the local-reader subagent exactly once to read local-read-fixture.txt, then answer with the mock result.'
        : (modelCase.toolRoundTrip ? 'Read README.md once, then answer with the mock result.' : 'Reply using the local mock.'),
      '--output-format',
      'json',
      '--no-session-persistence',
      '--prompt-suggestions',
      'false',
      '--disable-slash-commands',
      '--strict-mcp-config',
      '--mcp-config',
      '{"mcpServers":{}}',
      '--permission-mode',
      'dontAsk',
      '--tools',
      modelCase.agentRoundTrip ? 'default' : (modelCase.toolRoundTrip ? 'Read' : ''),
    ]
    if (modelCase.useBare !== false) {
      args.push('--bare')
    }
    if (modelCase.useSafeMode !== false) {
      args.push('--safe-mode')
    }
    if (modelCase.cliModel) {
      args.push('--model', modelCase.cliModel)
    }
    if (modelCase.toolRoundTrip) {
      args.push('--allowedTools', 'Read')
    }
    if (modelCase.agentRoundTrip) {
      args.push(
        '--allowedTools',
        'Read,Agent,Task',
        '--agents',
        JSON.stringify({
          'local-reader': {
            description: 'Read one local file when the user explicitly asks for the local reader.',
            prompt: 'Read only the requested local file with the Read tool, then report that the read completed.',
            tools: ['Read'],
          },
        }),
      )
    }

    const child = await runClaude(
      args,
      {
        HOME: tempHome,
        CLAUDE_CONFIG_DIR: path.join(tempHome, '.claude'),
        ANTHROPIC_BASE_URL: `http://127.0.0.1:${address.port}`,
        ...(modelCase.authMode === 'token'
          ? { ANTHROPIC_AUTH_TOKEN: localAPIKey }
          : { ANTHROPIC_API_KEY: localAPIKey }),
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
      modelCase.isolatedCwd || modelCase.agentRoundTrip ? tempHome : repoRoot,
    )
    assert.equal(child.timedOut, false, `${modelCase.name} timed out after ${processTimeoutMs}ms`)
    assert.equal(child.code, 0, `${modelCase.name} failed\nstdout: ${child.stdout}\nstderr: ${child.stderr}`)
    const result = JSON.parse(child.stdout)
    return { requests, countTokenRequests, requestMetadata, result, agentToolSchema, invocationArgs: args }
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

function writeAnthropicGenericToolUse(res, model, id, name, input) {
  writeSSE(res, [
    ['message_start', {
      type: 'message_start',
      message: {
        id: `msg_${id}`, type: 'message', role: 'assistant', model,
        content: [], stop_reason: null, stop_sequence: null,
        usage: { input_tokens: 5, output_tokens: 0 },
      },
    }],
    ['content_block_start', {
      type: 'content_block_start', index: 0,
      content_block: { type: 'tool_use', id, name, input: {} },
    }],
    ['content_block_delta', {
      type: 'content_block_delta', index: 0,
      delta: { type: 'input_json_delta', partial_json: JSON.stringify(input) },
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

function assertModelUsageIdentity(modelUsage, expectedModel, required = false) {
  if (modelUsage === undefined || modelUsage === null) {
    assert.equal(required, false, 'Claude Code result did not include modelUsage')
    return
  }
  assert.equal(typeof modelUsage, 'object')
  assert.deepEqual(Object.keys(modelUsage), [expectedModel])
}

function nativeGPTTemplateEnv() {
  return {
    ANTHROPIC_MODEL: 'gpt-5.6-terra',
    ANTHROPIC_CUSTOM_MODEL_OPTION: 'gpt-5.6-terra',
    ANTHROPIC_DEFAULT_OPUS_MODEL: 'gpt-5.6-sol',
    ANTHROPIC_DEFAULT_SONNET_MODEL: 'gpt-5.6-terra',
    ANTHROPIC_DEFAULT_HAIKU_MODEL: 'gpt-5.6-luna',
    CLAUDE_CODE_SUBAGENT_MODEL: 'inherit',
  }
}

async function assertNativeGPTTemplateMatchesUI() {
  const source = await readFile(path.join(repoRoot, 'frontend/src/components/keys/UseKeyModal.vue'), 'utf8')
  const constantsStart = source.indexOf('const OPENAI_CLAUDE_CODE_MODELS = {')
  const generatorStart = source.indexOf('function generateOpenAINativeClaudeFiles')
  const generatorEnd = source.indexOf('function generateAnthropicFiles', generatorStart)
  assert.ok(constantsStart >= 0 && generatorStart > constantsStart && generatorEnd > generatorStart, 'OpenAI Claude Code UI template was not found')

  const constantsBlock = source.slice(constantsStart, generatorStart)
  for (const [name, value] of Object.entries(nativeGPTTemplateEnv())) {
    assert.match(constantsBlock, new RegExp(`${escapeRegExp(name)}:\\s*'${escapeRegExp(value)}'`))
  }

  const generatorBlock = source.slice(generatorStart, generatorEnd)
  assert.match(generatorBlock, /ANTHROPIC_AUTH_TOKEN/)
  assert.doesNotMatch(generatorBlock, /ANTHROPIC_API_KEY/)
}

function assertLocalBearerRequests(requestMetadata) {
  assert.ok(requestMetadata.length > 0, 'expected at least one local mock request')
  for (const metadata of requestMetadata) {
    assert.equal(metadata.authorization, `Bearer ${localAPIKey}`)
    assert.equal(metadata.xApiKey, undefined)
    assert.match(metadata.host, /^127\.0\.0\.1:\d+$/)
    assert.equal(metadata.remoteAddress, '127.0.0.1')
  }
}

function findToolResult(request, toolUseID) {
  return (request.messages ?? []).some((message) =>
    Array.isArray(message.content) && message.content.some((part) => part.type === 'tool_result' && part.tool_use_id === toolUseID),
  )
}

function findToolUse(request, toolUseID) {
  for (const message of request.messages ?? []) {
    if (!Array.isArray(message.content)) {
      continue
    }
    const toolUse = message.content.find((part) => part.type === 'tool_use' && part.id === toolUseID)
    if (toolUse) {
      return toolUse
    }
  }
  return undefined
}

function requestContainsText(request, expectedText) {
  return (request.messages ?? []).some((message) => {
    if (typeof message.content === 'string') {
      return message.content.includes(expectedText)
    }
    return Array.isArray(message.content) && message.content.some((part) =>
      typeof part.text === 'string' && part.text.includes(expectedText),
    )
  })
}

function buildAgentToolInput(agentTool) {
  const properties = agentTool.input_schema?.properties ?? {}
  const required = agentTool.input_schema?.required ?? []
  const candidates = {
    description: 'Read local fixture',
    prompt: 'Read local-read-fixture.txt once with the Read tool, then report the mock result. Do not use network or modify files.',
    subagent_type: 'local-reader',
  }
  const input = Object.fromEntries(
    Object.entries(candidates).filter(([name]) => Object.hasOwn(properties, name)),
  )
  const unsupportedRequired = required.filter((name) => !Object.hasOwn(input, name))
  assert.deepEqual(unsupportedRequired, [], `unsupported required Agent/Task inputs: ${JSON.stringify(required)}`)
  assert.ok(Object.hasOwn(input, 'prompt'), `Agent/Task schema has no prompt input: ${JSON.stringify(agentTool.input_schema)}`)
  return input
}

function summarizeRequests(requests) {
  return requests.map((request) => ({
    model: request.model,
    toolNames: (request.tools ?? []).map((tool) => tool.name),
    messages: (request.messages ?? []).map((message) => ({
      role: message.role,
      content: Array.isArray(message.content)
        ? message.content.map((part) => ({
          type: part.type,
          name: part.name,
          content: part.type === 'tool_result' ? part.content : undefined,
          is_error: part.is_error,
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
