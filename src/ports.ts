import crypto from "node:crypto";
import fs from "node:fs/promises";
import net from "node:net";
import os from "node:os";
import path from "node:path";
import { withDirectoryLock } from "./lock.js";
import { isErrnoException, isRecord } from "./types.js";

interface HostPortReservation {
  assignmentId: string;
  statePath: string;
}

interface HostPortRegistry {
  version: 1;
  ports: Record<string, HostPortReservation>;
}

const hostPortRoot = path.join(os.tmpdir(), `ruk-host-${process.getuid?.() ?? "user"}`);

function isReservation(value: unknown): value is HostPortReservation {
  return isRecord(value) && typeof value["assignmentId"] === "string" && typeof value["statePath"] === "string";
}

async function reservationIsActive(port: number, reservation: HostPortReservation): Promise<boolean> {
  try {
    const state: unknown = JSON.parse(await fs.readFile(reservation.statePath, "utf8"));
    if (!isRecord(state) || !isRecord(state["workspaces"])) {
      throw new Error(`Invalid Ruk state in ${reservation.statePath}`);
    }
    return Object.values(state["workspaces"]).some((workspace) => {
      if (!isRecord(workspace) || !isRecord(workspace["assignment"])) return false;
      const assignment = workspace["assignment"];
      return assignment["id"] === reservation.assignmentId && isRecord(assignment["ports"]) &&
        Object.values(assignment["ports"]).includes(port);
    });
  } catch (error) {
    if (isErrnoException(error) && error.code === "ENOENT") return false;
    throw error;
  }
}

async function readHostPortRegistry(file: string): Promise<HostPortRegistry> {
  try {
    const value: unknown = JSON.parse(await fs.readFile(file, "utf8"));
    if (!isRecord(value) || value["version"] !== 1 || !isRecord(value["ports"])) {
      throw new Error(`Invalid Ruk host port registry in ${file}`);
    }
    const entries = Object.entries(value["ports"]);
    if (entries.some(([port, reservation]) => !/^\d+$/.test(port) || !isReservation(reservation))) {
      throw new Error(`Invalid Ruk host port registry in ${file}`);
    }
    return { version: 1, ports: Object.fromEntries(entries) as Record<string, HostPortReservation> };
  } catch (error) {
    if (isErrnoException(error) && error.code === "ENOENT") return { version: 1, ports: {} };
    throw error;
  }
}

async function writeHostPortRegistry(file: string, registry: HostPortRegistry): Promise<void> {
  const temporary = `${file}.${crypto.randomUUID()}.tmp`;
  try {
    await fs.writeFile(temporary, `${JSON.stringify(registry, null, 2)}\n`, { flag: "wx", mode: 0o600 });
    await fs.rename(temporary, file);
  } finally {
    await fs.rm(temporary, { force: true }).catch(() => {});
  }
}

async function ensureHostPortRoot(): Promise<void> {
  try {
    await fs.mkdir(hostPortRoot, { mode: 0o700 });
  } catch (error) {
    if (!isErrnoException(error) || error.code !== "EEXIST") throw error;
  }
  const stat = await fs.lstat(hostPortRoot);
  if (!stat.isDirectory() || stat.isSymbolicLink()) throw new Error(`Unsafe Ruk host port directory ${hostPortRoot}`);
  if (process.platform !== "win32" && (stat.uid !== process.getuid?.() || (stat.mode & 0o077) !== 0)) {
    throw new Error(`Unsafe Ruk host port directory ${hostPortRoot}`);
  }
}

export async function withHostPortRegistry<T>(
  callback: (registry: {
    reserved: ReadonlySet<number>;
    reserve(port: number, assignmentId: string, statePath: string): void;
    release(assignmentId: string): void;
    commit(): Promise<void>;
  }) => T | Promise<T>,
): Promise<T> {
  await ensureHostPortRoot();
  const file = path.join(hostPortRoot, "ports.json");
  return withDirectoryLock(path.join(hostPortRoot, "ports.lock"), async () => {
    const registry = await readHostPortRegistry(file);
    for (const [port, reservation] of Object.entries(registry.ports)) {
      if (!(await reservationIsActive(Number(port), reservation))) delete registry.ports[port];
    }
    let committed = false;
    const result = await callback({
      reserved: new Set(Object.keys(registry.ports).map(Number)),
      reserve(port, assignmentId, statePath) {
        if (committed) throw new Error("Host port registry is already committed");
        registry.ports[String(port)] = { assignmentId, statePath };
      },
      release(assignmentId) {
        if (committed) throw new Error("Host port registry is already committed");
        for (const [port, reservation] of Object.entries(registry.ports)) {
          if (reservation.assignmentId === assignmentId) delete registry.ports[port];
        }
      },
      async commit() {
        await writeHostPortRegistry(file, registry);
        committed = true;
      },
    });
    if (!committed) await writeHostPortRegistry(file, registry);
    return result;
  });
}

export async function releaseHostPorts(assignmentId: string): Promise<void> {
  await withHostPortRegistry((registry) => registry.release(assignmentId));
}

export function portEnvironmentName(name: string): string {
  const normalized = name.trim().toUpperCase().replace(/[^A-Z0-9]+/g, "_").replace(/^_+|_+$/g, "");
  if (!normalized) throw new Error("Port name must contain a letter or number");
  return `RUK_PORT_${normalized}`;
}

export function portEnvironment(ports: Readonly<Record<string, number>>): NodeJS.ProcessEnv {
  return Object.fromEntries(Object.entries(ports).map(([name, port]) => [portEnvironmentName(name), String(port)]));
}

export async function availablePort(excluded: ReadonlySet<number>): Promise<number> {
  for (let attempt = 0; attempt < 20; attempt += 1) {
    const port = await new Promise<number>((resolve, reject) => {
      const server = net.createServer();
      server.unref();
      server.once("error", reject);
      server.listen(0, "127.0.0.1", () => {
        const address = server.address();
        if (!address || typeof address === "string") {
          server.close(() => reject(new Error("Could not inspect allocated port")));
          return;
        }
        server.close((error) => error ? reject(error) : resolve(address.port));
      });
    });
    if (!excluded.has(port)) return port;
  }
  throw new Error("Could not allocate an available port");
}
