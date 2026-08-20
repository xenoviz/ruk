import type * as fsPromises from "node:fs/promises";

type LauncherFileSystem = Pick<
  typeof fsPromises,
  "mkdir" | "copyFile" | "writeFile" | "chmod" | "rename" | "rm"
>;

type WindowsReplacementOutput = {
  destination: string;
  mode?: number;
  executable?: boolean;
} & (
  | { source: string; contents?: never }
  | { contents: string; source?: never }
);

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

export declare function replaceWindowsOutputs(
  outputs: WindowsReplacementOutput[],
  fileSystem?: LauncherFileSystem,
): Promise<{ cleanupPending: boolean }>;

export declare function installNativeLauncher(options?: {
  root?: string | URL;
  platform?: string;
  arch?: string;
  libc?: string;
  commandDestination?: string;
  environment?: Record<string, string | undefined>;
  fileSystem?: LauncherFileSystem;
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
  cleanupPending: boolean;
  reused: boolean;
}>;

export declare function ensureNativeLauncher(options?: {
  root?: string | URL;
  platform?: string;
  arch?: string;
  libc?: string;
  commandDestination?: string;
  environment?: Record<string, string | undefined>;
  fileSystem?: LauncherFileSystem;
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
  cleanupPending: boolean;
  reused: boolean;
}>;

export declare function runPackageCommand(options?: {
  root?: string | URL;
  platform?: string;
  arch?: string;
  libc?: string;
  commandDestination?: string;
  environment?: Record<string, string | undefined>;
  args?: string[];
  exit?: (code: number) => void;
  writeError?: (message: string) => void;
  spawnSync?: (
    command: string,
    args: readonly string[],
    options: { stdio: "inherit"; env: NodeJS.ProcessEnv; windowsHide: boolean },
  ) => { status: number | null; signal: NodeJS.Signals | null; error?: Error };
  fileSystem?: LauncherFileSystem;
  spawnReplacement?: (
    command: string,
    args: string[],
    options: { detached: boolean; stdio: "ignore"; windowsHide: boolean },
  ) => { unref(): void };
}): Promise<{
  packageName?: string;
  target?: string;
  destination?: string;
  sha256?: string;
  installer?: "bun" | "npm" | "pnpm" | "yarn";
  deferred?: boolean;
  cleanupPending?: boolean;
  reused?: boolean;
  status: number;
  signal?: NodeJS.Signals;
  error?: string;
}>;
