export declare const NATIVE_TARGETS: Readonly<Record<string, {
  readonly packageName: string;
  readonly platform: string;
  readonly arch: string;
  readonly libc?: string;
}>>;

export declare function detectLibc(platform?: string, report?: {
  getReport?: () => { header?: { glibcVersionRuntime?: string } };
}): string | undefined;

export declare function platformTarget(
  platform?: string,
  arch?: string,
  libc?: string,
): { packageName: string; target: string };

export declare function installerFromEnvironment(
  environment?: Record<string, string | undefined>,
): "bun" | "npm" | "pnpm" | "yarn";

export declare function windowsCommandDestination(
  root: string,
  environment?: Record<string, string | undefined>,
): string;

export declare function windowsUpdateProcessID(
  environment?: Record<string, string | undefined>,
): number | undefined;

export declare function installNativeLauncher(options?: {
  root?: string;
  platform?: string;
  arch?: string;
  libc?: string;
  commandDestination?: string;
  environment?: Record<string, string | undefined>;
  spawnReplacement?: (
    command: string,
    args: string[],
    options: { detached: boolean; stdio: "ignore"; windowsHide: boolean },
  ) => { unref(): void };
}): Promise<{
  packageName: string;
  target: string;
  destination: string;
  sha256: string;
  installer: "bun" | "npm" | "pnpm" | "yarn";
  deferred: boolean;
}>;
