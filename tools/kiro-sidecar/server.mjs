#!/usr/bin/env node
import http from "node:http";
import { spawn } from "node:child_process";
import { createHash, randomUUID } from "node:crypto";

const port = Number.parseInt(process.env.PORT || "8787", 10);
const host = process.env.HOST || "127.0.0.1";
const sidecarMode = (process.env.KIRO_SIDECAR_MODE || "auto").toLowerCase();
const cliPath = process.env.KIRO_CLI_PATH || "kiro-cli";
const cliArgsTemplate = parseArgsTemplate();
const requestTimeoutMs = Number.parseInt(process.env.KIRO_REQUEST_TIMEOUT_MS || "120000", 10);
const httpTimeoutMs = Number.parseInt(process.env.KIRO_HTTP_TIMEOUT_MS || String(requestTimeoutMs), 10);
const maxBodyBytes = Number.parseInt(process.env.KIRO_MAX_BODY_BYTES || "2097152", 10);
const defaultRegion = process.env.KIRO_REGION || "us-east-1";
const defaultModel = "claude-sonnet-4-6";
const kiroVersion = process.env.KIRO_CLIENT_VERSION || "0.7.45";
const nodeRuntimeVersion = process.env.KIRO_NODE_VERSION || process.versions.node;
const systemVersion = process.env.KIRO_SYSTEM_VERSION || `${process.platform}-${process.arch}`;
const models = (process.env.KIRO_MODELS || "claude-opus-4-7,claude-sonnet-4-6,claude-haiku-4-5-20251001")
  .split(",")
  .map((v) => v.trim())
  .filter(Boolean);

const credentialCache = new Map();

const modelMap = new Map([
  ["claude-opus-4-7", "claude-opus-4.7"],
  ["claude-opus-4.7", "claude-opus-4.7"],
  ["claude-opus-4-6", "claude-opus-4.6"],
  ["claude-opus-4.6", "claude-opus-4.6"],
  ["claude-opus-4-5", "claude-opus-4.5"],
  ["claude-opus-4.5", "claude-opus-4.5"],
  ["claude-sonnet-4-6", "claude-sonnet-4.6"],
  ["claude-sonnet-4.6", "claude-sonnet-4.6"],
  ["claude-sonnet-4-5", "claude-sonnet-4.5"],
  ["claude-sonnet-4.5", "claude-sonnet-4.5"],
  ["claude-haiku-4-5-20251001", "claude-haiku-4.5"],
  ["claude-haiku-4-5", "claude-haiku-4.5"],
  ["claude-haiku-4.5", "claude-haiku-4.5"],
]);

function parseArgsTemplate() {
  const raw = process.env.KIRO_CLI_ARGS_JSON;
  if (!raw) return ["chat", "--no-interactive", "{prompt}"];
  const parsed = JSON.parse(raw);
  if (!Array.isArray(parsed) || parsed.some((item) => typeof item !== "string")) {
    throw new Error("KIRO_CLI_ARGS_JSON must be a JSON string array");
  }
  return parsed;
}

function sendJSON(res, status, body) {
  const data = JSON.stringify(body);
  res.writeHead(status, {
    "content-type": "application/json",
    "content-length": Buffer.byteLength(data),
  });
  res.end(data);
}

function sendError(res, status, type, message) {
  sendJSON(res, status, {
    type: "error",
    error: { type, message },
  });
}

function readBody(req) {
  return new Promise((resolve, reject) => {
    const chunks = [];
    let size = 0;
    req.on("data", (chunk) => {
      size += chunk.length;
      if (size > maxBodyBytes) {
        reject(new Error("request body too large"));
        req.destroy();
        return;
      }
      chunks.push(chunk);
    });
    req.on("end", () => resolve(Buffer.concat(chunks)));
    req.on("error", reject);
  });
}

function getHeader(req, name) {
  const value = req.headers[name.toLowerCase()];
  if (Array.isArray(value)) return value[0] || "";
  return value || "";
}

function hashText(value, length = 32) {
  return createHash("sha256").update(String(value || "")).digest("hex").slice(0, length);
}

function parseMaybeJSON(text) {
  const trimmed = String(text || "").trim();
  if (!trimmed) return null;
  if (trimmed.startsWith("{")) return JSON.parse(trimmed);
  return null;
}

function decodeCredentialText(raw) {
  let text = String(raw || "").trim();
  if (!text && process.env.KIRO_CREDENTIALS_JSON) text = process.env.KIRO_CREDENTIALS_JSON.trim();
  if (!text && process.env.KIRO_API_KEY) text = process.env.KIRO_API_KEY.trim();
  if (!text) return "";
  if (/^bearer\s+/i.test(text)) text = text.replace(/^bearer\s+/i, "").trim();
  if (text.startsWith("json:")) return text.slice(5).trim();
  if (text.startsWith("base64url:")) return Buffer.from(text.slice(10), "base64url").toString("utf8").trim();
  if (text.startsWith("base64:")) return Buffer.from(text.slice(7), "base64").toString("utf8").trim();

  try {
    const decoded = Buffer.from(text, "base64url").toString("utf8").trim();
    if (decoded.startsWith("{")) return decoded;
  } catch {
    // Keep the raw credential when it is not base64url JSON.
  }

  try {
    const decoded = Buffer.from(text, "base64").toString("utf8").trim();
    if (decoded.startsWith("{")) return decoded;
  } catch {
    // Keep the raw credential when it is not base64 JSON.
  }

  return text;
}

function parseTimestamp(value) {
  if (value === undefined || value === null || value === "") return 0;
  if (typeof value === "number") {
    return value > 10_000_000_000 ? value : value * 1000;
  }
  const asNumber = Number(value);
  if (Number.isFinite(asNumber)) return asNumber > 10_000_000_000 ? asNumber : asNumber * 1000;
  const parsed = Date.parse(String(value));
  return Number.isFinite(parsed) ? parsed : 0;
}

function parseCredential(raw) {
  const decoded = decodeCredentialText(raw);
  if (!decoded) return null;
  const json = parseMaybeJSON(decoded);
  const data = json && typeof json === "object" ? json : { refreshToken: decoded };
  const clientId = data.clientId || data.client_id || data.clientID || "";
  const clientSecret = data.clientSecret || data.client_secret || "";
  const authType = String(data.authType || data.auth_type || (clientId && clientSecret ? "aws_sso_oidc" : "desktop")).toLowerCase();

  return {
    cacheKey: hashText(decoded || raw, 48),
    raw: decoded || String(raw || ""),
    authType,
    accessToken: data.accessToken || data.access_token || data.token || "",
    refreshToken: data.refreshToken || data.refresh_token || data.refresh || "",
    clientId,
    clientSecret,
    region: data.region || data.apiRegion || data.api_region || defaultRegion,
    ssoRegion: data.ssoRegion || data.sso_region || data.region || defaultRegion,
    profileArn: data.profileArn || data.profile_arn || "",
    machineId: data.machineId || data.machine_id || data.fingerprint || data.uuid || "",
    endpointPreference: String(data.endpoint || data.preferredEndpoint || process.env.KIRO_ENDPOINT_ORDER || "auto").toLowerCase(),
    expiresAt: parseTimestamp(data.expiresAt || data.expires_at || data.expiration || data.expires),
  };
}

function isTokenFresh(credential) {
  return Boolean(credential?.accessToken) && (!credential.expiresAt || credential.expiresAt - Date.now() > 60_000);
}

async function resolveDirectCredential(rawCredential) {
  const parsed = parseCredential(rawCredential);
  if (!parsed) throw new Error("missing kiro credential");

  const cached = credentialCache.get(parsed.cacheKey);
  if (cached && isTokenFresh(cached)) {
    return { ...parsed, ...cached };
  }
  if (isTokenFresh(parsed)) {
    credentialCache.set(parsed.cacheKey, parsed);
    return parsed;
  }
  if (!parsed.refreshToken) {
    throw new Error("kiro credential must include accessToken or refreshToken");
  }

  const refreshed = await refreshKiroToken(parsed);
  const merged = { ...parsed, ...refreshed };
  credentialCache.set(parsed.cacheKey, merged);
  return merged;
}

async function refreshKiroToken(credential) {
  if (credential.authType.includes("sso") || credential.authType.includes("oidc")) {
    if (!credential.clientId || !credential.clientSecret) {
      throw new Error("aws_sso_oidc credential requires clientId and clientSecret");
    }
    const url = `https://oidc.${credential.ssoRegion || credential.region}.amazonaws.com/token`;
    const payload = {
      grantType: "refresh_token",
      clientId: credential.clientId,
      clientSecret: credential.clientSecret,
      refreshToken: credential.refreshToken,
    };
    const data = await postJSON(url, payload, { "content-type": "application/json" });
    return tokenRefreshResult(data, credential);
  }

  const url = `https://prod.${credential.region}.auth.desktop.kiro.dev/refreshToken`;
  const data = await postJSON(
    url,
    { refreshToken: credential.refreshToken },
    {
      "content-type": "application/json",
      "user-agent": `KiroIDE-${kiroVersion}${credential.machineId ? `-${credential.machineId}` : ""}`,
    },
  );
  return tokenRefreshResult(data, credential);
}

function tokenRefreshResult(data, credential) {
  const accessToken = data.accessToken || data.access_token;
  if (!accessToken) throw new Error("kiro token refresh did not return accessToken");
  const expiresIn = Number(data.expiresIn || data.expires_in || 3600);
  return {
    accessToken,
    refreshToken: data.refreshToken || data.refresh_token || credential.refreshToken,
    profileArn: data.profileArn || data.profile_arn || credential.profileArn,
    expiresAt: Date.now() + Math.max(60, expiresIn - 60) * 1000,
  };
}

async function postJSON(url, payload, headers) {
  const response = await fetchWithTimeout(url, {
    method: "POST",
    headers,
    body: JSON.stringify(payload),
  });
  const text = await response.text();
  let data = {};
  try {
    data = text ? JSON.parse(text) : {};
  } catch {
    data = { raw: text };
  }
  if (!response.ok) {
    throw new Error(`kiro auth HTTP ${response.status}: ${truncate(text, 500)}`);
  }
  return data;
}

async function fetchWithTimeout(url, options) {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), httpTimeoutMs);
  try {
    return await fetch(url, {
      ...options,
      signal: controller.signal,
    });
  } finally {
    clearTimeout(timer);
  }
}

function truncate(value, max) {
  const text = String(value || "");
  return text.length > max ? `${text.slice(0, max)}...` : text;
}

function mapKiroModel(model) {
  const lower = String(model || "").toLowerCase();
  for (const [key, value] of modelMap.entries()) {
    if (lower === key || lower.includes(key)) return value;
  }
  if (lower.startsWith("claude-")) return model;
  return mapKiroModel(defaultModel);
}

function extractPrompt(payload) {
  if (typeof payload.input === "string") return payload.input;
  if (Array.isArray(payload.input)) return flattenContent(payload.input);
  if (Array.isArray(payload.messages)) return flattenMessages(payload.messages);
  if (typeof payload.prompt === "string") return payload.prompt;
  return JSON.stringify(payload);
}

function flattenMessages(messages) {
  return messages
    .map((message) => {
      const role = typeof message.role === "string" ? message.role : "user";
      return `${role}: ${flattenContent(message.content)}`;
    })
    .join("\n");
}

function flattenContent(content) {
  const parsed = contentParts(content);
  return parsed.text;
}

function flattenContentForKiroPrompt(content) {
  if (typeof content === "string") return content;
  if (!Array.isArray(content)) {
    if (content && typeof content.text === "string") return content.text;
    if (content && typeof content.content === "string") return content.content;
    return "";
  }

  const text = [];
  for (const part of content) {
    if (typeof part === "string") {
      text.push(part);
      continue;
    }
    if (!part || typeof part !== "object") continue;

    if (part.type === "tool_use") {
      text.push(formatToolUseForKiroPrompt(part));
      continue;
    }
    if (part.type === "tool_result") {
      text.push(formatToolResultForKiroPrompt(part));
      continue;
    }

    if (typeof part.text === "string") text.push(part.text);
    else if (typeof part.content === "string") text.push(part.content);
    else if (part.type === "input_text" || part.type === "output_text") text.push(String(part.text || ""));
  }
  return text.filter(Boolean).join("\n");
}

function formatToolUseForKiroPrompt(part) {
  const id = String(part.id || part.tool_use_id || "");
  const name = safeKiroToolName(String(part.name || "tool"));
  const input = part.input && typeof part.input === "object" ? truncate(JSON.stringify(part.input), 8_000) : "{}";
  return [`[tool_use${id ? ` id=${id}` : ""} name=${name}]`, input].filter(Boolean).join("\n");
}

function formatToolResultForKiroPrompt(part) {
  const id = String(part.tool_use_id || part.id || "");
  const status = part.is_error ? "ERROR" : "SUCCESS";
  const resultText = flattenContentForKiroPrompt(part.content || part.text || "");
  return [`[tool_result${id ? ` id=${id}` : ""} status=${status}]`, resultText].filter(Boolean).join("\n");
}

function contentParts(content) {
  if (typeof content === "string") return { text: content, images: [], toolResults: [], toolUses: [] };
  if (!Array.isArray(content)) {
    if (content && typeof content.text === "string") return { text: content.text, images: [], toolResults: [], toolUses: [] };
    if (content && typeof content.content === "string") return { text: content.content, images: [], toolResults: [], toolUses: [] };
    return { text: "", images: [], toolResults: [], toolUses: [] };
  }

  const text = [];
  const images = [];
  const toolResults = [];
  const toolUses = [];

  for (const part of content) {
    if (typeof part === "string") {
      text.push(part);
      continue;
    }
    if (!part || typeof part !== "object") continue;
    if (typeof part.text === "string") text.push(part.text);
    else if (typeof part.content === "string" && part.type !== "tool_result") text.push(part.content);
    else if (part.type === "input_text" || part.type === "output_text") text.push(String(part.text || ""));

    if (part.type === "image" && part.source?.type === "base64" && part.source.data) {
      images.push({
        format: imageFormat(part.source.media_type),
        source: { bytes: part.source.data },
      });
    }

    if (part.type === "tool_result") {
      toolResults.push({
        toolUseId: String(part.tool_use_id || part.id || ""),
        status: part.is_error ? "ERROR" : "SUCCESS",
        content: [{ text: flattenContent(part.content || part.text || "") || "" }],
      });
    }

    if (part.type === "tool_use") {
      toolUses.push({
        toolUseId: String(part.id || part.tool_use_id || `toolu_${hashText(part.name || "", 12)}`),
        name: String(part.name || ""),
        input: part.input && typeof part.input === "object" ? part.input : {},
      });
    }
  }

  return {
    text: text.filter(Boolean).join("\n"),
    images,
    toolResults: toolResults.filter((item) => item.toolUseId),
    toolUses: toolUses.filter((item) => item.name),
  };
}

function imageFormat(mediaType) {
  const value = String(mediaType || "image/png").toLowerCase();
  if (value.includes("jpeg") || value.includes("jpg")) return "jpeg";
  if (value.includes("webp")) return "webp";
  if (value.includes("gif")) return "gif";
  return "png";
}

function estimateTokens(text) {
  return Math.max(1, Math.ceil(String(text || "").length / 4));
}

function normalizeMessages(path, payload) {
  if (path.endsWith("/chat/completions")) {
    const messages = Array.isArray(payload.messages) ? payload.messages : [];
    const system = messages
      .filter((message) => message.role === "system")
      .map((message) => flattenContent(message.content))
      .filter(Boolean)
      .join("\n\n");
    return {
      system,
      messages: messages.filter((message) => message.role !== "system"),
      tools: convertOpenAITools(payload.tools),
    };
  }

  if (path.endsWith("/responses")) {
    if (Array.isArray(payload.messages)) {
      return { system: extractSystemPrompt(payload.instructions || payload.system), messages: payload.messages, tools: [] };
    }
    return {
      system: extractSystemPrompt(payload.instructions || payload.system),
      messages: [{ role: "user", content: payload.input ?? payload.prompt ?? "" }],
      tools: [],
    };
  }

  return {
    system: extractSystemPrompt(payload.system),
    messages: Array.isArray(payload.messages) ? payload.messages : [{ role: "user", content: payload.prompt || payload.input || "" }],
    tools: convertClaudeTools(payload.tools),
  };
}

function extractSystemPrompt(system) {
  if (typeof system === "string") return system;
  if (!Array.isArray(system)) return "";
  return system.map((item) => flattenContent(item)).filter(Boolean).join("\n\n");
}

function convertClaudeTools(tools) {
  if (!Array.isArray(tools)) return [];
  return tools
    .map((tool) => makeKiroTool(tool?.name, tool?.description, tool?.input_schema || tool?.parameters))
    .filter(Boolean);
}

function convertOpenAITools(tools) {
  if (!Array.isArray(tools)) return [];
  return tools
    .map((tool) => {
      const fn = tool?.function || tool;
      return makeKiroTool(fn?.name, fn?.description, fn?.parameters || fn?.input_schema);
    })
    .filter(Boolean);
}

function makeKiroTool(name, description, schema) {
  const originalName = String(name || "").trim();
  if (!originalName) return null;
  const mappedName = safeKiroToolName(originalName);
  return {
    originalName,
    kiroName: mappedName,
    toolSpecification: {
      name: mappedName,
      description: truncate(String(description || ""), 10_000),
      inputSchema: { json: schema && typeof schema === "object" ? schema : { type: "object", properties: {} } },
    },
  };
}

function safeKiroToolName(name) {
  const normalized = String(name || "tool").replace(/[^a-zA-Z0-9_-]/g, "_");
  if (normalized.length <= 64) return normalized;
  return `${normalized.slice(0, 49)}_${hashText(normalized, 10)}`;
}

function buildKiroPayload(path, payload, req) {
  const requestedModel = typeof payload.model === "string" && payload.model.trim() ? payload.model.trim() : defaultModel;
  const kiroModel = mapKiroModel(requestedModel);
  const { system, messages, tools } = normalizeMessages(path, payload);
  const toolNameMap = new Map(tools.map((tool) => [tool.kiroName, tool.originalName]));
  const lastUserIndex = findCurrentMessageIndex(messages);
  const history = [];
  const promptAnchor = [];

  for (let i = 0; i < messages.length; i += 1) {
    const message = messages[i] || {};
    if (i === lastUserIndex) continue;
    if (message.role === "assistant") {
      const parts = contentParts(message.content);
      history.push({
        assistantResponseMessage: {
          content: flattenContentForKiroPrompt(message.content) || parts.text || ".",
        },
      });
      continue;
    }
    const parts = contentParts(message.content);
    const historyText = flattenContentForKiroPrompt(message.content) || parts.text;
    if (historyText) promptAnchor.push(historyText);
    const userMessage = {
      content: historyText || ".",
      modelId: kiroModel,
      origin: "AI_EDITOR",
      ...(parts.images.length ? { images: parts.images } : {}),
    };
    history.push({ userInputMessage: userMessage });
  }

  const current = messages[lastUserIndex] || { role: "user", content: extractPrompt(payload) };
  const currentParts = contentParts(current.content);
  const currentContext = {};
  if (tools.length) currentContext.tools = tools.map(({ toolSpecification }) => ({ toolSpecification }));
  const systemPrefix = system ? `--- SYSTEM PROMPT ---\n${system}\n--- END SYSTEM PROMPT ---\n\n` : "";
  const currentText = flattenContentForKiroPrompt(current.content) || currentParts.text || ".";
  const body = {
    conversationState: {
      chatTriggerType: "MANUAL",
      conversationId: buildConversationID(kiroModel, system, promptAnchor[0] || currentText, getHeader(req, "x-request-id")),
      currentMessage: {
        userInputMessage: {
          content: `${systemPrefix}${currentText}`,
          modelId: kiroModel,
          origin: "AI_EDITOR",
          ...(currentParts.images.length ? { images: currentParts.images } : {}),
          ...(Object.keys(currentContext).length ? { userInputMessageContext: currentContext } : {}),
        },
      },
      ...(dropLeadingAssistant(history).length ? { history: dropLeadingAssistant(history) } : {}),
    },
  };

  if (payload.profileArn || payload.profile_arn) body.profileArn = payload.profileArn || payload.profile_arn;

  const inferenceConfig = {};
  const maxTokens = Number(payload.max_tokens || payload.maxTokens || payload.max_completion_tokens || 0);
  if (maxTokens > 0) inferenceConfig.maxTokens = maxTokens;
  if (typeof payload.temperature === "number") inferenceConfig.temperature = payload.temperature;
  if (typeof payload.top_p === "number") inferenceConfig.topP = payload.top_p;
  if (Object.keys(inferenceConfig).length) body.inferenceConfig = inferenceConfig;

  return {
    body,
    requestedModel,
    kiroModel,
    inputTokens: estimateTokens(JSON.stringify(body)),
    promptText: `${system}\n${flattenMessages(messages)}`,
    toolNameMap,
  };
}

function findCurrentMessageIndex(messages) {
  for (let i = messages.length - 1; i >= 0; i -= 1) {
    const role = messages[i]?.role;
    if (role === "user" || role === "tool") return i;
  }
  return Math.max(0, messages.length - 1);
}

function dropLeadingAssistant(history) {
  let index = 0;
  while (history[index]?.assistantResponseMessage) index += 1;
  return history.slice(index);
}

function buildConversationID(model, system, anchor, requestID) {
  const stable = `${model}\n${system || ""}\n${anchor || ""}`;
  return `kiro-${hashText(stable || requestID || randomUUID(), 32)}`;
}

function getKiroEndpoints(credential) {
  const region = credential.region || defaultRegion;
  const endpoints = [
    {
      key: "codewhisperer",
      name: "CodeWhisperer",
      url: `https://codewhisperer.${region}.amazonaws.com/generateAssistantResponse`,
      origin: "AI_EDITOR",
      target: "AmazonCodeWhispererStreamingService.GenerateAssistantResponse",
      apiName: "codewhispererstreaming",
    },
    {
      key: "amazonq",
      name: "AmazonQ",
      url: `https://q.${region}.amazonaws.com/generateAssistantResponse`,
      origin: "CLI",
      target: "AmazonQDeveloperStreamingService.SendMessage",
      apiName: "codewhispererstreaming",
    },
  ];
  if (credential.endpointPreference === "amazonq") return [endpoints[1], endpoints[0]];
  if (credential.endpointPreference === "codewhisperer") return [endpoints[0], endpoints[1]];
  return endpoints;
}

function buildKiroHeaders(credential, endpoint) {
  const machineSuffix = credential.machineId ? `-${credential.machineId}` : "";
  const userAgent = `aws-sdk-js/1.0.34 ua/2.1 os/${systemVersion} lang/js md/nodejs#${nodeRuntimeVersion} api/${endpoint.apiName}#1.0.34 m/E KiroIDE-${kiroVersion}${machineSuffix}`;
  return {
    "authorization": `Bearer ${credential.accessToken}`,
    "content-type": "application/json",
    "accept": "*/*",
    "x-amz-target": endpoint.target,
    "user-agent": userAgent,
    "x-amz-user-agent": `aws-sdk-js/1.0.34 KiroIDE-${kiroVersion}${machineSuffix}`,
    "x-amzn-codewhisperer-optout": "true",
    "x-amzn-kiro-agent-mode": "vibe",
    "amz-sdk-request": "attempt=1; max=3",
    "amz-sdk-invocation-id": randomUUID(),
  };
}

async function callKiroDirect(rawCredential, requestContext, callbacks) {
  const credential = await resolveDirectCredential(rawCredential);
  const endpoints = getKiroEndpoints(credential);
  let lastError = null;

  for (const endpoint of endpoints) {
    const upstreamPayload = structuredClone(requestContext.body);
    upstreamPayload.conversationState.currentMessage.userInputMessage.origin = endpoint.origin;
    if (credential.profileArn && !upstreamPayload.profileArn) upstreamPayload.profileArn = credential.profileArn;

    try {
      const response = await fetchWithTimeout(endpoint.url, {
        method: "POST",
        headers: buildKiroHeaders(credential, endpoint),
        body: JSON.stringify(upstreamPayload),
      });
      if (response.status === 429 || response.status >= 500) {
        lastError = new Error(`kiro ${endpoint.name} HTTP ${response.status}: ${truncate(await response.text(), 500)}`);
        continue;
      }
      if (!response.ok) {
        const body = await response.text();
        throw new Error(`kiro ${endpoint.name} HTTP ${response.status}: ${truncate(body, 500)}`);
      }
      return await parseKiroEventStream(response.body, callbacks);
    } catch (err) {
      lastError = err;
      if (String(err?.message || "").includes("HTTP 401") || String(err?.message || "").includes("HTTP 403")) {
        throw err;
      }
    }
  }

  throw lastError || new Error("all kiro endpoints failed");
}

async function parseKiroEventStream(body, callbacks = {}) {
  if (!body) throw new Error("kiro upstream response has no body");
  const reader = body.getReader();
  let buffer = Buffer.alloc(0);
  const state = {
    lastAssistant: "",
    lastReasoning: "",
    toolUse: null,
    usage: { inputTokens: 0, outputTokens: 0, credits: 0 },
  };

  while (true) {
    const { done, value } = await reader.read();
    if (done) break;
    buffer = Buffer.concat([buffer, Buffer.from(value)]);
    while (buffer.length >= 16) {
      const totalLength = buffer.readUInt32BE(0);
      const headersLength = buffer.readUInt32BE(4);
      if (totalLength <= 0 || totalLength > 16 * 1024 * 1024) throw new Error(`invalid AWS event-stream frame length ${totalLength}`);
      if (buffer.length < totalLength) break;
      const headersStart = 12;
      const headersEnd = headersStart + headersLength;
      const payloadStart = headersEnd;
      const payloadEnd = totalLength - 4;
      if (headersEnd <= buffer.length && payloadStart < payloadEnd) {
        const eventType = extractEventType(buffer.subarray(headersStart, headersEnd));
        const payloadBytes = buffer.subarray(payloadStart, payloadEnd);
        try {
          const event = JSON.parse(payloadBytes.toString("utf8"));
          processKiroEvent(eventType, event, state, callbacks);
        } catch (err) {
          if (!(err instanceof SyntaxError)) throw err;
        }
      }
      buffer = buffer.subarray(totalLength);
    }
  }

  finishToolUse(state, callbacks);
  callbacks.onComplete?.(state.usage);
  return state.usage;
}

function extractEventType(headers) {
  let offset = 0;
  while (offset < headers.length) {
    const nameLength = headers[offset];
    offset += 1;
    if (offset + nameLength > headers.length) break;
    const name = headers.subarray(offset, offset + nameLength).toString("utf8");
    offset += nameLength;
    if (offset >= headers.length) break;
    const valueType = headers[offset];
    offset += 1;

    if (valueType === 7) {
      if (offset + 2 > headers.length) break;
      const valueLength = headers.readUInt16BE(offset);
      offset += 2;
      if (offset + valueLength > headers.length) break;
      const value = headers.subarray(offset, offset + valueLength).toString("utf8");
      offset += valueLength;
      if (name === ":event-type") return value;
      continue;
    }

    if (valueType === 6) {
      if (offset + 2 > headers.length) break;
      const valueLength = headers.readUInt16BE(offset);
      offset += 2 + valueLength;
    } else {
      offset += ({ 0: 0, 1: 0, 2: 1, 3: 2, 4: 4, 5: 8, 8: 8, 9: 16 }[valueType] ?? headers.length);
    }
  }
  return "";
}

function processKiroEvent(eventType, event, state, callbacks) {
  updateUsageFromEvent(event, state.usage);
  const typed = event[eventType] && typeof event[eventType] === "object" ? event[eventType] : event;

  if (event._type || event.error || eventType === "error") {
    throw new Error(event.message || event.error?.message || "kiro stream error");
  }

  if (eventType === "assistantResponseEvent" || event.assistantResponseEvent || typeof typed.content === "string") {
    const delta = normalizeDelta(String(typed.content || ""), "lastAssistant", state);
    if (delta) callbacks.onText?.(delta, false);
  }

  if (eventType === "reasoningContentEvent" || event.reasoningContentEvent) {
    const delta = normalizeDelta(String(typed.text || typed.content || ""), "lastReasoning", state);
    if (delta) callbacks.onText?.(delta, true);
  }

  if (eventType === "toolUseEvent" || event.toolUseEvent || typed.toolUseId || typed.name || typed.input !== undefined || typed.stop !== undefined) {
    handleToolUseEvent(typed, state, callbacks);
  }

  if (eventType === "meteringEvent" || event.meteringEvent) {
    const usage = Number(typed.usage || 0);
    if (Number.isFinite(usage)) state.usage.credits += usage;
  }
}

function normalizeDelta(value, stateKey, state) {
  if (!value) return "";
  const previous = state[stateKey] || "";
  if (!previous) {
    state[stateKey] = value;
    return value;
  }
  if (value === previous || previous.startsWith(value)) return "";
  if (value.startsWith(previous)) {
    state[stateKey] = value;
    return value.slice(previous.length);
  }
  let overlap = 0;
  const max = Math.min(previous.length, value.length);
  for (let i = max; i > 0; i -= 1) {
    if (previous.endsWith(value.slice(0, i))) {
      overlap = i;
      break;
    }
  }
  state[stateKey] = value;
  return overlap ? value.slice(overlap) : value;
}

function handleToolUseEvent(event, state, callbacks) {
  const toolUseId = event.toolUseId || event.id || state.toolUse?.toolUseId || "";
  const name = event.name || state.toolUse?.name || "";
  if (toolUseId && name && (!state.toolUse || state.toolUse.toolUseId !== toolUseId)) {
    finishToolUse(state, callbacks);
    state.toolUse = { toolUseId, name, inputBuffer: "" };
  }
  if (!state.toolUse && (toolUseId || name)) {
    state.toolUse = { toolUseId: toolUseId || `toolu_${hashText(name, 12)}`, name: name || "tool", inputBuffer: "" };
  }
  if (state.toolUse && event.input !== undefined) {
    state.toolUse.inputBuffer += typeof event.input === "string" ? event.input : JSON.stringify(event.input);
  }
  if (event.stop === true) finishToolUse(state, callbacks);
}

function finishToolUse(state, callbacks) {
  if (!state.toolUse) return;
  let input = {};
  if (state.toolUse.inputBuffer) {
    try {
      input = JSON.parse(state.toolUse.inputBuffer);
    } catch {
      input = { _partialInput: state.toolUse.inputBuffer };
    }
  }
  callbacks.onToolUse?.({
    toolUseId: state.toolUse.toolUseId,
    name: state.toolUse.name,
    input,
  });
  state.toolUse = null;
}

function updateUsageFromEvent(event, usage) {
  for (const obj of collectObjects(event)) {
    const input = readNumber(obj, "inputTokens", "promptTokens", "totalInputTokens", "input_tokens", "prompt_tokens");
    const output = readNumber(obj, "outputTokens", "completionTokens", "totalOutputTokens", "output_tokens", "completion_tokens");
    const uncached = readNumber(obj, "uncachedInputTokens", "uncached_input_tokens");
    const cacheRead = readNumber(obj, "cacheReadInputTokens", "cache_read_input_tokens");
    const cacheWrite = readNumber(obj, "cacheWriteInputTokens", "cache_write_input_tokens", "cacheCreationInputTokens", "cache_creation_input_tokens");
    if (input > 0) usage.inputTokens = input;
    if (output > 0) usage.outputTokens = output;
    if (uncached + cacheRead + cacheWrite > 0) usage.inputTokens = uncached + cacheRead + cacheWrite;
  }
}

function collectObjects(value, out = []) {
  if (!value || typeof value !== "object") return out;
  if (!Array.isArray(value)) out.push(value);
  for (const child of Array.isArray(value) ? value : Object.values(value)) collectObjects(child, out);
  return out;
}

function readNumber(obj, ...keys) {
  for (const key of keys) {
    const raw = obj?.[key];
    if (raw === undefined || raw === null) continue;
    const n = Number(raw);
    if (Number.isFinite(n)) return Math.trunc(n);
  }
  return 0;
}

async function runKiroDirectNonStream(rawCredential, requestContext) {
  let text = "";
  const toolUses = [];
  let finalUsage = { inputTokens: requestContext.inputTokens, outputTokens: 0, credits: 0 };
  await callKiroDirect(rawCredential, requestContext, {
    onText: (delta) => {
      text += delta;
    },
    onToolUse: (tool) => toolUses.push(restoreToolUseName(tool, requestContext.toolNameMap)),
    onComplete: (usage) => {
      finalUsage = {
        inputTokens: usage.inputTokens || requestContext.inputTokens,
        outputTokens: usage.outputTokens || estimateTokens(text),
        credits: usage.credits || 0,
      };
    },
  });
  return { text, toolUses, usage: finalUsage };
}

function restoreToolUseName(tool, toolNameMap) {
  return {
    ...tool,
    name: toolNameMap.get(tool.name) || tool.name,
  };
}

async function streamKiroDirect(rawCredential, requestContext, res, path) {
  if (path.endsWith("/chat/completions")) return streamOpenAI(rawCredential, requestContext, res);
  if (path.endsWith("/responses")) return streamResponses(rawCredential, requestContext, res);
  return streamClaude(rawCredential, requestContext, res);
}

function writeSSE(res, event, data) {
  if (event) res.write(`event: ${event}\n`);
  res.write(`data: ${JSON.stringify(data)}\n\n`);
}

async function streamClaude(rawCredential, requestContext, res) {
  const id = `msg_${Date.now().toString(36)}`;
  let textOpen = false;
  let textStarted = false;
  let nextIndex = 0;
  let outputText = "";
  let usedTool = false;

  res.writeHead(200, {
    "content-type": "text/event-stream; charset=utf-8",
    "cache-control": "no-cache",
    "connection": "keep-alive",
  });
  writeSSE(res, "message_start", {
    type: "message_start",
    message: {
      id,
      type: "message",
      role: "assistant",
      model: requestContext.requestedModel,
      content: [],
      stop_reason: null,
      stop_sequence: null,
      usage: { input_tokens: requestContext.inputTokens, output_tokens: 0 },
    },
  });

  const ensureTextBlock = () => {
    if (textStarted) return;
    writeSSE(res, "content_block_start", {
      type: "content_block_start",
      index: nextIndex,
      content_block: { type: "text", text: "" },
    });
    textStarted = true;
    textOpen = true;
  };

  const closeTextBlock = () => {
    if (!textOpen) return;
    writeSSE(res, "content_block_stop", { type: "content_block_stop", index: nextIndex });
    textOpen = false;
    nextIndex += 1;
  };

  try {
    await callKiroDirect(rawCredential, requestContext, {
      onText: (delta) => {
        if (!delta) return;
        ensureTextBlock();
        outputText += delta;
        writeSSE(res, "content_block_delta", {
          type: "content_block_delta",
          index: nextIndex,
          delta: { type: "text_delta", text: delta },
        });
      },
      onToolUse: (tool) => {
        closeTextBlock();
        usedTool = true;
        const restored = restoreToolUseName(tool, requestContext.toolNameMap);
        writeSSE(res, "content_block_start", {
          type: "content_block_start",
          index: nextIndex,
          content_block: { type: "tool_use", id: restored.toolUseId, name: restored.name, input: {} },
        });
        writeSSE(res, "content_block_delta", {
          type: "content_block_delta",
          index: nextIndex,
          delta: { type: "input_json_delta", partial_json: JSON.stringify(restored.input || {}) },
        });
        writeSSE(res, "content_block_stop", { type: "content_block_stop", index: nextIndex });
        nextIndex += 1;
      },
      onComplete: (usage) => {
        closeTextBlock();
        writeSSE(res, "message_delta", {
          type: "message_delta",
          delta: { stop_reason: usedTool ? "tool_use" : "end_turn", stop_sequence: null },
          usage: { output_tokens: usage.outputTokens || estimateTokens(outputText) },
        });
        writeSSE(res, "message_stop", { type: "message_stop" });
        res.end();
      },
    });
  } catch (err) {
    writeSSE(res, "error", { type: "error", error: { type: "upstream_error", message: err instanceof Error ? err.message : "kiro stream failed" } });
    res.end();
  }
}

async function streamOpenAI(rawCredential, requestContext, res) {
  const id = `chatcmpl_${Date.now().toString(36)}`;
  res.writeHead(200, {
    "content-type": "text/event-stream; charset=utf-8",
    "cache-control": "no-cache",
    "connection": "keep-alive",
  });
  res.write(`data: ${JSON.stringify({ id, object: "chat.completion.chunk", created: nowSeconds(), model: requestContext.requestedModel, choices: [{ index: 0, delta: { role: "assistant" }, finish_reason: null }] })}\n\n`);
  try {
    await callKiroDirect(rawCredential, requestContext, {
      onText: (delta) => {
        if (!delta) return;
        res.write(`data: ${JSON.stringify({ id, object: "chat.completion.chunk", created: nowSeconds(), model: requestContext.requestedModel, choices: [{ index: 0, delta: { content: delta }, finish_reason: null }] })}\n\n`);
      },
      onToolUse: (tool) => {
        const restored = restoreToolUseName(tool, requestContext.toolNameMap);
        res.write(`data: ${JSON.stringify({ id, object: "chat.completion.chunk", created: nowSeconds(), model: requestContext.requestedModel, choices: [{ index: 0, delta: { tool_calls: [{ index: 0, id: restored.toolUseId, type: "function", function: { name: restored.name, arguments: JSON.stringify(restored.input || {}) } }] }, finish_reason: null }] })}\n\n`);
      },
      onComplete: () => {
        res.write(`data: ${JSON.stringify({ id, object: "chat.completion.chunk", created: nowSeconds(), model: requestContext.requestedModel, choices: [{ index: 0, delta: {}, finish_reason: "stop" }] })}\n\n`);
        res.write("data: [DONE]\n\n");
        res.end();
      },
    });
  } catch (err) {
    res.write(`data: ${JSON.stringify({ error: { message: err instanceof Error ? err.message : "kiro stream failed", type: "upstream_error" } })}\n\n`);
    res.write("data: [DONE]\n\n");
    res.end();
  }
}

async function streamResponses(rawCredential, requestContext, res) {
  const id = `resp_${Date.now().toString(36)}`;
  res.writeHead(200, {
    "content-type": "text/event-stream; charset=utf-8",
    "cache-control": "no-cache",
    "connection": "keep-alive",
  });
  writeSSE(res, "response.created", { type: "response.created", response: { id, status: "in_progress", model: requestContext.requestedModel } });
  try {
    await callKiroDirect(rawCredential, requestContext, {
      onText: (delta) => {
        if (delta) writeSSE(res, "response.output_text.delta", { type: "response.output_text.delta", item_id: id, output_index: 0, content_index: 0, delta });
      },
      onComplete: () => {
        writeSSE(res, "response.completed", { type: "response.completed", response: { id, status: "completed", model: requestContext.requestedModel } });
        res.end();
      },
    });
  } catch (err) {
    writeSSE(res, "response.failed", { type: "response.failed", response: { id, status: "failed" }, error: { message: err instanceof Error ? err.message : "kiro stream failed" } });
    res.end();
  }
}

function buildArgs(prompt, model) {
  return cliArgsTemplate.flatMap((arg) => {
    const replaced = arg.replaceAll("{prompt}", prompt).replaceAll("{model}", model);
    return replaced === "" ? [] : [replaced];
  });
}

function redactSecret(value, secret) {
  const text = String(value || "");
  if (!secret || secret.length < 8) return text;
  return text.split(secret).join("[redacted]");
}

function runKiroCLI({ apiKey, prompt, model }) {
  return new Promise((resolve, reject) => {
    const effectiveAPIKey = apiKey || process.env.KIRO_API_KEY || "";
    const child = spawn(cliPath, buildArgs(prompt, model), {
      env: {
        ...process.env,
        KIRO_API_KEY: effectiveAPIKey,
      },
      stdio: ["ignore", "pipe", "pipe"],
    });
    let stdout = "";
    let stderr = "";
    const timer = setTimeout(() => {
      child.kill("SIGTERM");
      reject(new Error("kiro cli timed out"));
    }, requestTimeoutMs);

    child.stdout.setEncoding("utf8");
    child.stderr.setEncoding("utf8");
    child.stdout.on("data", (chunk) => {
      stdout += chunk;
    });
    child.stderr.on("data", (chunk) => {
      stderr += chunk;
    });
    child.on("error", (err) => {
      clearTimeout(timer);
      reject(err);
    });
    child.on("close", (code) => {
      clearTimeout(timer);
      if (code !== 0) {
        reject(new Error(redactSecret(stderr.trim() || `kiro cli exited with ${code}`, effectiveAPIKey)));
        return;
      }
      resolve(redactSecret(stdout.trim(), effectiveAPIKey));
    });
  });
}

function modelsResponse() {
  return {
    object: "list",
    data: models.map((id) => ({
      id,
      object: "model",
      type: "model",
      display_name: displayNameForModel(id),
    })),
  };
}

function displayNameForModel(id) {
  return id
    .split("-")
    .filter((part) => part && !/^\d{8}$/.test(part))
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
    .join(" ");
}

function claudeContentBlocks(text, toolUses = []) {
  const content = [];
  if (text) content.push({ type: "text", text });
  for (const tool of toolUses) {
    content.push({ type: "tool_use", id: tool.toolUseId, name: tool.name, input: tool.input || {} });
  }
  return content.length ? content : [{ type: "text", text: "" }];
}

function messageResponse(model, text, inputTokens, toolUses = [], usage = {}) {
  return {
    id: `msg_${Date.now().toString(36)}`,
    type: "message",
    role: "assistant",
    model,
    content: claudeContentBlocks(text, toolUses),
    stop_reason: toolUses.length ? "tool_use" : "end_turn",
    stop_sequence: null,
    usage: {
      input_tokens: usage.inputTokens || inputTokens,
      output_tokens: usage.outputTokens || estimateTokens(text),
    },
  };
}

function chatCompletionsResponse(model, text, inputTokens, toolUses = [], usage = {}) {
  const message = { role: "assistant", content: text };
  if (toolUses.length) {
    message.tool_calls = toolUses.map((tool) => ({
      id: tool.toolUseId,
      type: "function",
      function: { name: tool.name, arguments: JSON.stringify(tool.input || {}) },
    }));
  }
  return {
    id: `chatcmpl_${Date.now().toString(36)}`,
    object: "chat.completion",
    created: nowSeconds(),
    model,
    choices: [{ index: 0, message, finish_reason: toolUses.length ? "tool_calls" : "stop" }],
    usage: {
      prompt_tokens: usage.inputTokens || inputTokens,
      completion_tokens: usage.outputTokens || estimateTokens(text),
      total_tokens: (usage.inputTokens || inputTokens) + (usage.outputTokens || estimateTokens(text)),
    },
  };
}

function responsesResponse(model, text, inputTokens, usage = {}) {
  return {
    id: `resp_${Date.now().toString(36)}`,
    object: "response",
    status: "completed",
    model,
    output: [{ type: "message", role: "assistant", content: [{ type: "output_text", text }] }],
    output_text: text,
    usage: {
      input_tokens: usage.inputTokens || inputTokens,
      output_tokens: usage.outputTokens || estimateTokens(text),
    },
  };
}

function nowSeconds() {
  return Math.floor(Date.now() / 1000);
}

async function handleInference(req, res, path) {
  const raw = await readBody(req);
  let payload;
  try {
    payload = JSON.parse(raw.toString("utf8"));
  } catch {
    sendError(res, 400, "invalid_request_error", "request body must be JSON");
    return;
  }

  const apiKey = getHeader(req, "x-kiro-api-key");
  const requestContext = buildKiroPayload(path, payload, req);
  const prompt = extractPrompt(payload).trim();
  const shouldTryDirect = sidecarMode !== "cli";
  const allowCLIFallback = sidecarMode === "auto" && payload.stream !== true;

  if (shouldTryDirect) {
    try {
      if (payload.stream === true) {
        await streamKiroDirect(apiKey, requestContext, res, path);
        return;
      }
      const result = await runKiroDirectNonStream(apiKey, requestContext);
      if (path.endsWith("/chat/completions")) {
        sendJSON(res, 200, chatCompletionsResponse(requestContext.requestedModel, result.text, requestContext.inputTokens, result.toolUses, result.usage));
      } else if (path.endsWith("/responses")) {
        sendJSON(res, 200, responsesResponse(requestContext.requestedModel, result.text, requestContext.inputTokens, result.usage));
      } else {
        sendJSON(res, 200, messageResponse(requestContext.requestedModel, result.text, requestContext.inputTokens, result.toolUses, result.usage));
      }
      return;
    } catch (err) {
      if (!allowCLIFallback) {
        const message = err instanceof Error ? err.message : "kiro direct call failed";
        sendError(res, 502, "upstream_error", redactSecret(message, apiKey));
        return;
      }
      process.stderr.write(`[kiro-sidecar] direct mode failed, falling back to cli: ${redactSecret(err instanceof Error ? err.message : String(err), apiKey)}\n`);
    }
  }

  if (payload.stream === true) {
    sendError(res, 400, "invalid_request_error", "cli fallback does not implement streaming");
    return;
  }
  if (!prompt) {
    sendError(res, 400, "invalid_request_error", "prompt/messages/input is required");
    return;
  }

  try {
    const text = await runKiroCLI({ apiKey, prompt, model: requestContext.requestedModel });
    const inputTokens = estimateTokens(prompt);
    if (path.endsWith("/chat/completions")) {
      sendJSON(res, 200, chatCompletionsResponse(requestContext.requestedModel, text, inputTokens));
    } else if (path.endsWith("/responses")) {
      sendJSON(res, 200, responsesResponse(requestContext.requestedModel, text, inputTokens));
    } else {
      sendJSON(res, 200, messageResponse(requestContext.requestedModel, text, inputTokens));
    }
  } catch (err) {
    const message = err instanceof Error ? err.message : "kiro cli failed";
    sendError(res, 502, "upstream_error", redactSecret(message, apiKey));
  }
}

async function handleCountTokens(req, res) {
  const raw = await readBody(req);
  let payload;
  try {
    payload = JSON.parse(raw.toString("utf8"));
  } catch {
    sendError(res, 400, "invalid_request_error", "request body must be JSON");
    return;
  }
  sendJSON(res, 200, { input_tokens: estimateTokens(extractPrompt(payload)) });
}

const server = http.createServer(async (req, res) => {
  try {
    const path = new URL(req.url || "/", `http://${req.headers.host || "localhost"}`).pathname;
    if (req.method === "GET" && path === "/healthz") {
      sendJSON(res, 200, {
        status: "ok",
        mode: sidecarMode,
        direct: sidecarMode !== "cli",
        cli: sidecarMode !== "direct" ? cliPath : undefined,
        region: defaultRegion,
        models,
      });
      return;
    }
    if (req.method === "GET" && (path === "/v1/models" || path === "/models")) {
      sendJSON(res, 200, modelsResponse());
      return;
    }
    if (req.method === "POST" && path === "/v1/messages/count_tokens") {
      await handleCountTokens(req, res);
      return;
    }
    if (req.method === "POST" && ["/v1/messages", "/v1/chat/completions", "/v1/responses"].includes(path)) {
      await handleInference(req, res, path);
      return;
    }
    sendError(res, 404, "not_found_error", "route not found");
  } catch (err) {
    if (!res.headersSent) {
      sendError(res, 500, "api_error", err instanceof Error ? err.message : "sidecar error");
      return;
    }
    res.end();
  }
});

server.listen(port, host, () => {
  process.stderr.write(`[kiro-sidecar] listening on http://${host}:${port} mode=${sidecarMode}\n`);
});
