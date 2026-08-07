import net from "node:net";

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
