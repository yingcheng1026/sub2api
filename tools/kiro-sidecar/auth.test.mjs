import assert from "node:assert/strict";
import test from "node:test";

import { authorizeEnvironmentCredentialFallback, hasEnvironmentCredential } from "./auth.mjs";

const sharedCredentialEnv = {
  KIRO_API_KEY: "shared-upstream-credential",
  KIRO_SIDECAR_AUTH_TOKEN: "0123456789abcdef0123456789abcdef",
};

test("detects process-level Kiro credentials", () => {
  assert.equal(hasEnvironmentCredential({}), false);
  assert.equal(hasEnvironmentCredential({ KIRO_API_KEY: "  token  " }), true);
  assert.equal(hasEnvironmentCredential({ KIRO_CREDENTIALS_JSON: "{}" }), true);
});

test("caller-supplied upstream credentials do not use the shared fallback", () => {
  const result = authorizeEnvironmentCredentialFallback({
    providedCredential: "caller-owned-credential",
    authorizationHeader: "",
    env: { KIRO_API_KEY: "shared-upstream-credential" },
  });
  assert.deepEqual(result, { allowed: true });
});

test("shared fallback fails closed when its caller token is absent or too short", () => {
  const missing = authorizeEnvironmentCredentialFallback({
    providedCredential: "",
    authorizationHeader: "",
    env: { KIRO_API_KEY: "shared-upstream-credential" },
  });
  assert.equal(missing.allowed, false);
  assert.equal(missing.status, 503);

  const short = authorizeEnvironmentCredentialFallback({
    providedCredential: "",
    authorizationHeader: "Bearer tiny",
    env: { KIRO_API_KEY: "shared-upstream-credential", KIRO_SIDECAR_AUTH_TOKEN: "tiny" },
  });
  assert.equal(short.allowed, false);
  assert.equal(short.status, 503);
});

test("shared fallback rejects the wrong bearer token and accepts the configured token", () => {
  const rejected = authorizeEnvironmentCredentialFallback({
    providedCredential: "",
    authorizationHeader: "Bearer wrong-token",
    env: sharedCredentialEnv,
  });
  assert.equal(rejected.allowed, false);
  assert.equal(rejected.status, 401);

  const accepted = authorizeEnvironmentCredentialFallback({
    providedCredential: "",
    authorizationHeader: `Bearer ${sharedCredentialEnv.KIRO_SIDECAR_AUTH_TOKEN}`,
    env: sharedCredentialEnv,
  });
  assert.deepEqual(accepted, { allowed: true });
});
