// @ts-check
import { createHash, generateKeyPairSync, randomBytes, sign } from "node:crypto";
import { readFileSync } from "node:fs";
import http from "node:http";
import { createMockUsers } from "./data.mjs";

const port = Number(process.env.MOCK_OIDC_PORT ?? 4300);
const host = process.env.MOCK_OIDC_HOST ?? "127.0.0.1";
const issuer = `http://127.0.0.1:${port}/realms/custos`;
const clientId = process.env.MOCK_OIDC_CLIENT_ID ?? "custos-web";
const clientSecret = process.env.MOCK_OIDC_CLIENT_SECRET ?? readFileSync(new URL("./client-secret.txt", import.meta.url), "utf8").trim();
const autoLogin = process.env.MOCK_OIDC_AUTO_LOGIN;
const tokenTtl = Math.max(1, Number(process.env.MOCK_OIDC_TOKEN_TTL ?? 120) || 120);
const users = createMockUsers(issuer);
const { privateKey, publicKey } = generateKeyPairSync("rsa", { modulusLength: 2048 });
const publicJwk = { ...publicKey.export({ format: "jwk" }), kid: "mock-key", use: "sig", alg: "RS256" };
/** @typedef {{ state: string, nonce: string, codeChallenge: string, redirectUri: string, username?: keyof typeof users, code?: string }} AuthorizationTransaction */
/** @type {Map<string, AuthorizationTransaction>} */
const authorizations = new Map();
/** @type {Map<string, keyof typeof users>} */
const refreshTokens = new Map();
/** @type {Map<string, keyof typeof users>} */
const accessTokens = new Map();
let tokenCounter = 0;

/** @param {http.ServerResponse} response @param {number} status @param {unknown} body @param {http.OutgoingHttpHeaders} [headers] */
function sendJson(response, status, body, headers = {}) {
  response.writeHead(status, { "content-type": "application/json; charset=utf-8", "cache-control": "no-store", ...headers });
  response.end(JSON.stringify(body));
}

/** @param {http.ServerResponse} response @param {number} status @param {string} body */
function sendHtml(response, status, body) {
  response.writeHead(status, { "content-type": "text/html; charset=utf-8", "cache-control": "no-store", "x-content-type-options": "nosniff" });
  response.end(body);
}

/** @param {http.IncomingMessage} request */
async function readForm(request) {
  /** @type {Buffer[]} */
  const chunks = [];
  for await (const chunk of request) chunks.push(Buffer.from(chunk));
  return new URLSearchParams(Buffer.concat(chunks).toString("utf8"));
}

/** @param {object} claims */
function createJwt(claims) {
  const header = Buffer.from(JSON.stringify({ alg: "RS256", typ: "JWT", kid: publicJwk.kid })).toString("base64url");
  const payload = Buffer.from(JSON.stringify(claims)).toString("base64url");
  const content = `${header}.${payload}`;
  return `${content}.${sign("RSA-SHA256", Buffer.from(content), privateKey).toString("base64url")}`;
}

/** @param {keyof typeof users} username */
function issueAccessToken(username) {
  tokenCounter += 1;
  const token = `mock-access-${username}-${tokenCounter}`;
  accessTokens.set(token, username);
  return token;
}

/** @param {keyof typeof users} username */
function issueTokenSet(username) {
  const accessToken = issueAccessToken(username);
  tokenCounter += 1;
  const refreshToken = `mock-refresh-${username}-${tokenCounter}`;
  refreshTokens.set(refreshToken, username);
  return { access_token: accessToken, refresh_token: refreshToken, token_type: "Bearer", expires_in: tokenTtl };
}

/** @param {string} state @param {keyof typeof users} username @param {http.ServerResponse} response */
function completeAuthorization(state, username, response) {
  const transaction = authorizations.get(state);
  if (!transaction) return sendJson(response, 400, { error: "invalid_state" });
  authorizations.delete(state);
  const code = randomBytes(32).toString("base64url");
  transaction.username = username;
  transaction.code = code;
  authorizations.set(code, transaction);
  const callback = new URL(transaction.redirectUri);
  callback.searchParams.set("code", code);
  callback.searchParams.set("state", state);
  response.writeHead(302, { location: callback.toString(), "cache-control": "no-store" });
  response.end();
}

/** @param {string} state */
function pickerHtml(state) {
  const choices = [
    ["alice", "Alice Researcher"],
    ["admin", "Platform Admin"],
    ["bob", "Bob Newcomer"],
  ];
  const buttons = choices.map(([value, label]) =>
    `<form method="post" action="/realms/custos/protocol/openid-connect/auth/select"><input type="hidden" name="state" value="${state}"><button type="submit" name="user" value="${value}">${label}</button></form>`,
  ).join("\n");
  return `<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>Mock sign in</title><main><h1>Choose a mock user</h1><p>This local-only identity provider issues no real credentials.</p>${buttons}</main></html>`;
}

/** @param {http.IncomingMessage} request @param {http.ServerResponse} response */
async function handle(request, response) {
  const url = new URL(request.url ?? "/", `http://127.0.0.1:${port}`);
  const realmPath = "/realms/custos";
  if (url.pathname === "/health") return sendJson(response, 200, { status: "ok" });
  if (url.pathname === `${realmPath}/.well-known/openid-configuration`) {
    return sendJson(response, 200, {
      issuer,
      authorization_endpoint: `${issuer}/protocol/openid-connect/auth`,
      token_endpoint: `${issuer}/protocol/openid-connect/token`,
      jwks_uri: `${issuer}/protocol/openid-connect/certs`,
      userinfo_endpoint: `${issuer}/protocol/openid-connect/userinfo`,
      end_session_endpoint: `${issuer}/protocol/openid-connect/logout`,
      revocation_endpoint: `${issuer}/protocol/openid-connect/revoke`,
      response_types_supported: ["code"],
      subject_types_supported: ["public"],
      id_token_signing_alg_values_supported: ["RS256"],
      token_endpoint_auth_methods_supported: ["client_secret_basic", "client_secret_post"],
      code_challenge_methods_supported: ["S256"],
    });
  }
  if (url.pathname === `${realmPath}/protocol/openid-connect/certs`) return sendJson(response, 200, { keys: [publicJwk] });

  if (url.pathname === `${realmPath}/protocol/openid-connect/auth` && request.method === "GET") {
    const state = url.searchParams.get("state");
    const nonce = url.searchParams.get("nonce");
    const codeChallenge = url.searchParams.get("code_challenge");
    const redirectUri = url.searchParams.get("redirect_uri");
    if (
      !state || !nonce || !codeChallenge ||
      url.searchParams.get("code_challenge_method") !== "S256" ||
      !redirectUri || url.searchParams.get("client_id") !== clientId
    ) return sendJson(response, 400, { error: "invalid_authorization_request" });

    authorizations.set(state, { state, nonce, codeChallenge, redirectUri });
    if (autoLogin && Object.hasOwn(users, autoLogin)) {
      return completeAuthorization(state, /** @type {keyof typeof users} */ (autoLogin), response);
    }
    return sendHtml(response, 200, pickerHtml(state));
  }

  if (url.pathname === `${realmPath}/protocol/openid-connect/auth/select` && request.method === "POST") {
    const parameters = await readForm(request);
    const state = parameters.get("state") ?? "";
    const username = parameters.get("user") ?? "";
    if (!Object.hasOwn(users, username)) return sendJson(response, 400, { error: "invalid_user" });
    return completeAuthorization(state, /** @type {keyof typeof users} */ (username), response);
  }

  if (url.pathname === `${realmPath}/protocol/openid-connect/token` && request.method === "POST") {
    const parameters = await readForm(request);
    const authorization = request.headers.authorization ?? "";
    const basic = typeof authorization === "string" && authorization.startsWith("Basic ")
      ? Buffer.from(authorization.slice(6), "base64").toString("utf8").split(":")
      : [];
    const requestClientId = parameters.get("client_id") ?? basic[0];
    const requestClientSecret = parameters.get("client_secret") ?? basic[1];
    if (requestClientId !== clientId || requestClientSecret !== clientSecret) {
      return sendJson(response, 401, { error: "invalid_client" });
    }

    if (parameters.get("grant_type") === "refresh_token") {
      const oldRefreshToken = parameters.get("refresh_token") ?? "";
      const username = refreshTokens.get(oldRefreshToken);
      if (!username) return sendJson(response, 400, { error: "invalid_grant" });
      return sendJson(response, 200, issueTokenSet(username));
    }

    const code = parameters.get("code") ?? "";
    const transaction = authorizations.get(code);
    const verifier = parameters.get("code_verifier") ?? "";
    const challenge = createHash("sha256").update(verifier).digest("base64url");
    if (
      parameters.get("grant_type") !== "authorization_code" ||
      !transaction ||
      transaction.codeChallenge !== challenge ||
      transaction.redirectUri !== parameters.get("redirect_uri")
    ) return sendJson(response, 400, { error: "invalid_grant" });

    authorizations.delete(code);
    const username = transaction.username;
    if (!username || !Object.hasOwn(users, username)) return sendJson(response, 400, { error: "invalid_grant" });
    const tokens = issueTokenSet(username);
    const now = Math.floor(Date.now() / 1000);
    const idToken = createJwt({
      iss: issuer,
      aud: clientId,
      sub: users[username].sub,
      nonce: transaction.nonce,
      iat: now,
      exp: now + 300,
      name: users[username].name,
      email: users[username].email,
    });
    return sendJson(response, 200, { ...tokens, id_token: idToken, scope: "openid profile email" });
  }

  if (url.pathname === `${realmPath}/protocol/openid-connect/userinfo` && request.method === "GET") {
    const authorization = request.headers.authorization;
    const accessToken = typeof authorization === "string" ? authorization.replace(/^Bearer /, "") : "";
    const username = accessTokens.get(accessToken);
    if (!username) return sendJson(response, 401, { error: "invalid_token" });
    return sendJson(response, 200, { sub: users[username].sub, name: users[username].name, email: users[username].email });
  }

  if (url.pathname === `${realmPath}/protocol/openid-connect/revoke` && request.method === "POST") {
    const parameters = await readForm(request);
    const token = parameters.get("token") ?? "";
    const username = refreshTokens.get(token);
    if (username) {
      for (const [refreshToken, owner] of refreshTokens) {
        if (owner === username) refreshTokens.delete(refreshToken);
      }
      for (const [accessToken, owner] of accessTokens) {
        if (owner === username) accessTokens.delete(accessToken);
      }
    }
    response.writeHead(200, { "cache-control": "no-store" });
    return response.end();
  }

  if (url.pathname === `${realmPath}/protocol/openid-connect/logout`) {
    const target = url.searchParams.get("post_logout_redirect_uri");
    if (!target) {
      response.writeHead(204, { "cache-control": "no-store" });
      return response.end();
    }
    const destination = new URL(target);
    const state = url.searchParams.get("state");
    if (state) destination.searchParams.set("state", state);
    response.writeHead(302, { location: destination.toString(), "cache-control": "no-store" });
    return response.end();
  }

  return sendJson(response, 404, { error: "not_found" });
}

const server = http.createServer((request, response) => {
  void handle(request, response).catch(() => {
    if (!response.headersSent) sendJson(response, 500, { error: "mock_oidc_error" });
    else response.destroy();
  });
});
server.listen(port, host, () => process.stdout.write(`mock OIDC ready at ${issuer}\n`));
