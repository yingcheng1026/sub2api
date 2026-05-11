#!/usr/bin/env node
import http from "node:http";
import { spawn } from "node:child_process";

const port = Number.parseInt(process.env.PORT || "8787", 10);
const host = process.env.HOST || "127.0.0.1";
const cliPath = process.env.KIRO_CLI_PATH || "kiro-cli";
const cliArgsTemplate = parseArgsTemplate();
const requestTimeoutMs = Number.parseInt(process.env.KIRO_REQUEST_TIMEOUT_MS || "90000", 10);
const maxBodyBytes = Number.parseInt(process.env.KIRO_MAX_BODY_BYTES || "2097152", 10);
const defaultModel = "claude-sonnet-4-6";
const models = (process.env.KIRO_MODELS || "claude-opus-4-7,claude-sonnet-4-6,claude-haiku-4-5-20251001")
  .split(",")
  .map((v) => v.trim())
  .filter(Boolean);

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
        req.destroy(new Error("request body too large"));
        return;
      }
      chunks.push(chunk);
    });
    req.on("end", () => resolve(Buffer.concat(chunks)));
    req.on("error", reject);
  });
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
  if (typeof content === "string") return content;
  if (!Array.isArray(content)) return "";
  return content
    .map((part) => {
      if (typeof part === "string") return part;
      if (part && typeof part.text === "string") return part.text;
      if (part && typeof part.content === "string") return part.content;
      return "";
    })
    .filter(Boolean)
    .join("\n");
}

function estimateTokens(text) {
  return Math.max(1, Math.ceil(String(text || "").length / 4));
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

function runKiro({ apiKey, prompt, model }) {
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

function messageResponse(model, text, inputTokens) {
  return {
    id: `msg_${Date.now().toString(36)}`,
    type: "message",
    role: "assistant",
    model,
    content: [{ type: "text", text }],
    usage: {
      input_tokens: inputTokens,
      output_tokens: estimateTokens(text),
    },
  };
}

function chatCompletionsResponse(model, text, inputTokens) {
  return {
    id: `chatcmpl_${Date.now().toString(36)}`,
    object: "chat.completion",
    created: Math.floor(Date.now() / 1000),
    model,
    choices: [{ index: 0, message: { role: "assistant", content: text }, finish_reason: "stop" }],
    usage: {
      prompt_tokens: inputTokens,
      completion_tokens: estimateTokens(text),
      total_tokens: inputTokens + estimateTokens(text),
    },
  };
}

function responsesResponse(model, text, inputTokens) {
  return {
    id: `resp_${Date.now().toString(36)}`,
    object: "response",
    status: "completed",
    model,
    output: [{ type: "message", role: "assistant", content: [{ type: "output_text", text }] }],
    output_text: text,
    usage: {
      input_tokens: inputTokens,
      output_tokens: estimateTokens(text),
    },
  };
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
  if (payload.stream === true) {
    sendError(res, 400, "invalid_request_error", "this reference sidecar does not implement streaming");
    return;
  }
  const model = typeof payload.model === "string" && payload.model.trim() ? payload.model.trim() : defaultModel;
  const prompt = extractPrompt(payload).trim();
  if (!prompt) {
    sendError(res, 400, "invalid_request_error", "prompt/messages/input is required");
    return;
  }

  try {
    const apiKey = req.headers["x-kiro-api-key"];
    const text = await runKiro({ apiKey: Array.isArray(apiKey) ? apiKey[0] : apiKey, prompt, model });
    const inputTokens = estimateTokens(prompt);
    if (path.endsWith("/chat/completions")) {
      sendJSON(res, 200, chatCompletionsResponse(model, text, inputTokens));
    } else if (path.endsWith("/responses")) {
      sendJSON(res, 200, responsesResponse(model, text, inputTokens));
    } else {
      sendJSON(res, 200, messageResponse(model, text, inputTokens));
    }
  } catch (err) {
    const apiKey = req.headers["x-kiro-api-key"];
    const secret = Array.isArray(apiKey) ? apiKey[0] : apiKey;
    const message = err instanceof Error ? err.message : "kiro cli failed";
    sendError(res, 502, "upstream_error", redactSecret(message, secret));
  }
}

const server = http.createServer(async (req, res) => {
  try {
    const path = new URL(req.url || "/", `http://${req.headers.host || "localhost"}`).pathname;
    if (req.method === "GET" && path === "/healthz") {
      sendJSON(res, 200, { status: "ok", cli: cliPath, models });
      return;
    }
    if (req.method === "GET" && path === "/v1/models") {
      sendJSON(res, 200, modelsResponse());
      return;
    }
    if (req.method === "POST" && ["/v1/messages", "/v1/chat/completions", "/v1/responses"].includes(path)) {
      await handleInference(req, res, path);
      return;
    }
    sendError(res, 404, "not_found_error", "route not found");
  } catch (err) {
    sendError(res, 500, "api_error", err instanceof Error ? err.message : "sidecar error");
  }
});

server.listen(port, host, () => {
  process.stderr.write(`[kiro-sidecar] listening on http://${host}:${port}\n`);
});
