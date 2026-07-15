import { createHash, timingSafeEqual } from "node:crypto";

const minimumAuthTokenBytes = 32;

function trimmed(value) {
  return String(value || "").trim();
}

function extractBearerToken(value) {
  const match = /^Bearer\s+(.+)$/i.exec(trimmed(value));
  return match ? match[1].trim() : "";
}

function constantTimeTokenEqual(actual, expected) {
  const actualDigest = createHash("sha256").update(actual).digest();
  const expectedDigest = createHash("sha256").update(expected).digest();
  return timingSafeEqual(actualDigest, expectedDigest);
}

export function hasEnvironmentCredential(env = process.env) {
  return Boolean(trimmed(env.KIRO_CREDENTIALS_JSON) || trimmed(env.KIRO_API_KEY));
}

export function authorizeEnvironmentCredentialFallback({
  providedCredential,
  authorizationHeader,
  env = process.env,
}) {
  if (trimmed(providedCredential) || !hasEnvironmentCredential(env)) {
    return { allowed: true };
  }

  const expected = trimmed(env.KIRO_SIDECAR_AUTH_TOKEN);
  if (Buffer.byteLength(expected, "utf8") < minimumAuthTokenBytes) {
    return {
      allowed: false,
      status: 503,
      type: "configuration_error",
      message: "environment credential fallback is disabled until KIRO_SIDECAR_AUTH_TOKEN is configured with at least 32 bytes",
    };
  }

  const actual = extractBearerToken(authorizationHeader);
  if (!actual || !constantTimeTokenEqual(actual, expected)) {
    return {
      allowed: false,
      status: 401,
      type: "authentication_error",
      message: "valid sidecar authorization is required for environment credentials",
    };
  }
  return { allowed: true };
}
