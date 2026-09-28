// @ts-check
import { randomBytes } from "node:crypto";
import { readFileSync } from "node:fs";
import { spawn, spawnSync } from "node:child_process";
import { resolve } from "node:path";
import { setTimeout as delay } from "node:timers/promises";
import { fileURLToPath } from "node:url";

const root = resolve(fileURLToPath(new URL("..", import.meta.url)));
const nextBin = resolve(root, "node_modules/next/dist/bin/next");
const secretFile = resolve(root, "mock/client-secret.txt");
const clientSecret = readFileSync(secretFile, "utf8").trim();
const webArgs = process.argv.slice(2);
const parsed = parseWebArgs(webArgs);
const webPort = Number(process.env.PORT ?? parsed.port ?? 3000);
const oidcPort = Number(process.env.MOCK_OIDC_PORT ?? 4300);
const apiPort = Number(process.env.MOCK_API_PORT ?? 4301);
const publicOrigin = `http://localhost:${webPort}`;
const issuer = `http://127.0.0.1:${oidcPort}/realms/custos`;
const oidcHost = parsed.hostname === "0.0.0.0" ? "0.0.0.0" : "127.0.0.1";
const nextArguments = ["dev", ...parsed.forwarded, "--hostname", parsed.hostname, "--port", String(webPort)];

/** @typedef {{ name: string, child: import("node:child_process").ChildProcess }} ChildRecord */
/** @type {ChildRecord[]} */
const children = [];
let stopping = false;
/** @type {Promise<void> | undefined} */
let stopPromise;

/** @param {string[]} args */
function parseWebArgs(args) {
  let hostname = "localhost";
  /** @type {string | undefined} */
  let port;
  /** @type {string[]} */
  const forwarded = [];
  for (let index = 0; index < args.length; index += 1) {
    const argument = args[index];
    if (argument === "--hostname" && args[index + 1]) {
      hostname = args[++index];
    } else if (argument.startsWith("--hostname=")) {
      hostname = argument.slice("--hostname=".length);
    } else if (argument === "--port" && args[index + 1]) {
      port = args[++index];
    } else if (argument.startsWith("--port=")) {
      port = argument.slice("--port=".length);
    } else {
      forwarded.push(argument);
    }
  }
  return { hostname, port, forwarded };
}

/** @param {ChildRecord} record */
function hasExited(record) {
  return record.child.exitCode !== null || record.child.signalCode !== null;
}

/** @param {ChildRecord} record */
function killTree(record) {
  const pid = record.child.pid;
  if (!pid || hasExited(record)) return;
  if (process.platform === "win32") {
    spawnSync("taskkill", ["/PID", String(pid), "/T", "/F"], { stdio: "ignore" });
    return;
  }
  try {
    process.kill(-pid, "SIGTERM");
  } catch {
    record.child.kill("SIGTERM");
  }
}

/** @param {ChildRecord} record */
function killTreeSync(record) {
  const pid = record.child.pid;
  if (!pid || hasExited(record)) return;
  if (process.platform === "win32") {
    spawnSync("taskkill", ["/PID", String(pid), "/T", "/F"], { stdio: "ignore" });
    return;
  }
  try {
    process.kill(-pid, "SIGKILL");
  } catch {
    record.child.kill("SIGKILL");
  }
}

/** @param {ChildRecord} record @returns {Promise<void>} */
function waitForExit(record) {
  if (hasExited(record)) return Promise.resolve();
  return new Promise((resolveExit) => record.child.once("exit", () => resolveExit()));
}

/** @param {number} exitCode */
function shutdown(exitCode) {
  if (stopPromise) return stopPromise;
  stopping = true;
  process.exitCode = exitCode;
  stopPromise = (async () => {
    const running = children.filter((record) => !hasExited(record));
    for (const record of running) killTree(record);
    await Promise.all(running.map((record) => Promise.race([waitForExit(record), delay(2500)])));
    for (const record of children) killTreeSync(record);
  })();
  return stopPromise;
}

/** @param {string} name @param {string} script @param {NodeJS.ProcessEnv} env */
function startNodeChild(name, script, env) {
  return startChild(name, process.execPath, [script], env);
}

/** @param {string} name @param {string} command @param {string[]} args @param {NodeJS.ProcessEnv} env */
function startChild(name, command, args, env) {
  const child = spawn(command, args, {
    cwd: root,
    env,
    stdio: "inherit",
    detached: process.platform !== "win32",
    shell: false,
  });
  const record = { name, child };
  children.push(record);
  child.once("error", (error) => {
    if (!stopping) {
      process.stderr.write(`${name} failed to start: ${error.message}\n`);
      void shutdown(1);
    }
  });
  child.once("exit", (code, signal) => {
    if (!stopping) {
      process.stderr.write(`${name} exited unexpectedly (${signal ?? code ?? "unknown"})\n`);
      void shutdown(name === "next" && code === 0 ? 0 : 1);
    }
  });
  return record;
}

/** @param {string} name @param {string} url @param {ChildRecord} child */
async function waitForHealth(name, url, child) {
  const deadline = Date.now() + 30_000;
  while (Date.now() < deadline) {
    if (stopping || hasExited(child)) throw new Error(`${name} exited before becoming healthy`);
    try {
      const response = await fetch(url, { signal: AbortSignal.timeout(1000) });
      if (response.ok) return;
    } catch {
      await delay(150);
    }
  }
  throw new Error(`${name} health check timed out`);
}

process.on("SIGINT", () => void shutdown(130));
process.on("SIGTERM", () => void shutdown(143));
process.on("exit", () => {
  for (const record of children) killTreeSync(record);
});

try {
  const oidcEnv = {
    ...process.env,
    MOCK_OIDC_PORT: String(oidcPort),
    MOCK_OIDC_HOST: oidcHost,
    MOCK_OIDC_CLIENT_ID: "custos-web",
    MOCK_OIDC_CLIENT_SECRET: clientSecret,
  };
  const apiEnv = {
    ...process.env,
    MOCK_API_PORT: String(apiPort),
    MOCK_OIDC_ISSUER: issuer,
  };
  /** @type {NodeJS.ProcessEnv} */
  const nextEnv = {
    ...process.env,
    NODE_ENV: "development",
    PORT: String(webPort),
    HOSTNAME: parsed.hostname,
    AUTH_URL: publicOrigin,
    NEXTAUTH_URL: publicOrigin,
    CUSTOS_WEB_ISSUER: issuer,
    CUSTOS_WEB_CLIENT_ID: "custos-web",
    CUSTOS_WEB_CLIENT_SECRET_FILE: secretFile,
    CUSTOS_WEB_PUBLIC_ORIGIN: publicOrigin,
    CUSTOS_API_URL: `http://127.0.0.1:${apiPort}`,
    CUSTOS_WEB_AUTH_SECRETS: randomBytes(32).toString("base64"),
    CUSTOS_WEB_DEV: "1",
    CUSTOS_WEB_INSECURE_COOKIES: "1",
  };
  delete nextEnv.CUSTOS_API_CA_FILE;
  delete nextEnv.CUSTOS_WEB_CA_FILE;

  const oidcChild = startNodeChild("mock OIDC", "mock/oidc-server.mjs", oidcEnv);
  const apiChild = startNodeChild("mock API", "mock/api-server.mjs", apiEnv);
  await Promise.all([
    waitForHealth("mock OIDC", `http://127.0.0.1:${oidcPort}/health`, oidcChild),
    waitForHealth("mock API", `http://127.0.0.1:${apiPort}/health`, apiChild),
  ]);
  process.stdout.write(`Mock mode: ${publicOrigin} — Alice Researcher (alice), Platform Admin (admin), Bob Newcomer (bob)\n`);

  const next = startChild("next", process.execPath, [nextBin, ...nextArguments], nextEnv);
  await waitForExit(next);
  await shutdown(typeof process.exitCode === "number" ? process.exitCode : 0);
} catch (error) {
  process.stderr.write(`${error instanceof Error ? error.message : String(error)}\n`);
  await shutdown(1);
}
