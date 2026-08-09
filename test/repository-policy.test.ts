import assert from "node:assert/strict";
import http, { type IncomingMessage, type ServerResponse } from "node:http";
import path from "node:path";
import test, { type TestContext } from "node:test";
import { run } from "../src/process.js";
import { isRecord } from "../src/types.js";

const root = process.cwd();
const script = path.join(root, "scripts", "configure-repository.ts");

interface Mutation {
  method: string;
  name: string;
}

async function requestBody(request: IncomingMessage): Promise<unknown> {
  const chunks: Buffer[] = [];
  for await (const chunk of request) {
    chunks.push(Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk));
  }
  const text = Buffer.concat(chunks).toString("utf8");
  return text ? JSON.parse(text) as unknown : null;
}

function respond(response: ServerResponse, status: number, body: unknown): void {
  response.writeHead(status, { "content-type": "application/json" });
  response.end(JSON.stringify(body));
}

async function githubApi(
  t: TestContext,
  existing: readonly Record<string, unknown>[],
  failOnName?: string,
): Promise<{ url: string; mutations: Mutation[] }> {
  const mutations: Mutation[] = [];
  const ids = new Map([
    ["Protect main", 101],
    ["Require main CI", 102],
    ["Protect release tags", 103],
  ]);
  const server = http.createServer((request, response) => {
    void (async () => {
      const method = request.method ?? "GET";
      const url = request.url ?? "";
      if (method === "PATCH" && url === "/repos/test/repository") {
        await requestBody(request);
        respond(response, 200, { name: "repository" });
        return;
      }
      if (method === "GET" && url === "/repos/test/repository/rulesets?includes_parents=false") {
        respond(response, 200, existing);
        return;
      }
      if ((method === "POST" || method === "PUT") && url.startsWith("/repos/test/repository/rulesets")) {
        const body = await requestBody(request);
        if (!isRecord(body) || typeof body["name"] !== "string") {
          respond(response, 400, { message: "invalid ruleset" });
          return;
        }
        const name = body["name"];
        mutations.push({ method, name });
        if (name === failOnName) {
          respond(response, 500, { message: "simulated failure" });
          return;
        }
        respond(response, 200, { id: ids.get(name) ?? 999, name });
        return;
      }
      respond(response, 404, { message: `${method} ${url} not found` });
    })().catch((error: unknown) => {
      respond(response, 500, { message: error instanceof Error ? error.message : String(error) });
    });
  });

  await new Promise<void>((resolve, reject) => {
    const onError = (error: Error): void => reject(error);
    server.once("error", onError);
    server.listen(0, "127.0.0.1", () => {
      server.off("error", onError);
      resolve();
    });
  });
  t.after(() => new Promise<void>((resolve, reject) => {
    server.close((error) => error ? reject(error) : resolve());
  }));
  const address = server.address();
  if (!address || typeof address === "string") throw new Error("Mock GitHub API has no TCP address");
  return { url: `http://127.0.0.1:${address.port}`, mutations };
}

function environment(apiUrl: string): NodeJS.ProcessEnv {
  return {
    ...process.env,
    RUK_GITHUB_ADMIN_TOKEN: "test-token",
    RUK_GITHUB_API_URL: apiUrl,
    RUK_GITHUB_REPOSITORY: "test/repository",
  };
}

test("repository policy preview lists fail-closed application order", async () => {
  const result = await run("bun", [script], { cwd: root });
  const preview: unknown = JSON.parse(result.stdout);
  assert.ok(isRecord(preview));
  const rulesets = preview["rulesets"];
  assert.ok(Array.isArray(rulesets) && rulesets.every(isRecord));
  assert.deepEqual(rulesets.map((ruleset) => ruleset["name"]), [
    "Require main CI",
    "Protect main",
    "Protect release tags",
  ]);
});

test("repository policy creates required CI before weakening existing protection", async (t) => {
  const mock = await githubApi(t, [
    { id: 101, name: "Protect main" },
    { id: 103, name: "Protect release tags" },
  ]);
  await run("bun", [script, "--apply"], { cwd: root, env: environment(mock.url) });
  assert.deepEqual(mock.mutations, [
    { method: "POST", name: "Require main CI" },
    { method: "PUT", name: "Protect main" },
    { method: "PUT", name: "Protect release tags" },
  ]);
});

test("repository policy stops before weakening main when CI creation fails", async (t) => {
  const mock = await githubApi(t, [
    { id: 101, name: "Protect main" },
    { id: 103, name: "Protect release tags" },
  ], "Require main CI");
  await assert.rejects(
    run("bun", [script, "--apply"], { cwd: root, env: environment(mock.url) }),
    /simulated failure/,
  );
  assert.deepEqual(mock.mutations, [{ method: "POST", name: "Require main CI" }]);
});

test("repository policy updates every existing ruleset in fail-closed order", async (t) => {
  const mock = await githubApi(t, [
    { id: 101, name: "Protect main" },
    { id: 102, name: "Require main CI" },
    { id: 103, name: "Protect release tags" },
  ]);
  await run("bun", [script, "--apply"], { cwd: root, env: environment(mock.url) });
  assert.deepEqual(mock.mutations, [
    { method: "PUT", name: "Require main CI" },
    { method: "PUT", name: "Protect main" },
    { method: "PUT", name: "Protect release tags" },
  ]);
});
