import path from "node:path";

const UUID = /\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b/gi;
const ISO_TIMESTAMP = /\b\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{3,9})?Z\b/g;
const PROCESS_IDENTITY = /\b(?:PID|process(?: ID)?)\s+\d+\b/gi;
const GENERATED_WORKSPACE = /<repo>-[A-Za-z0-9._-]+/g;

export interface NormalizationContext {
  roots?: readonly string[];
}

function replaceRoot(value: string, root: string): string {
  const variants = new Set([root, root.replaceAll("\\", "/"), root.replaceAll("/", "\\")]);
  let result = value;
  for (const variant of variants) {
    if (!variant) continue;
    result = result.replaceAll(variant, "<repo>");
  }
  return result;
}

export function normalizeText(value: string, context: NormalizationContext = {}): string {
  let result = value.replaceAll("\r\n", "\n");
  for (const root of context.roots ?? []) result = replaceRoot(result, root);
  result = result.replaceAll("\\", "/");
  result = result.replace(GENERATED_WORKSPACE, "<workspace>");
  result = result.replace(UUID, "<uuid>");
  result = result.replace(ISO_TIMESTAMP, "<timestamp>");
  return result.replace(PROCESS_IDENTITY, "process <pid>");
}

function keyIsPID(key: string | undefined): boolean {
  return key !== undefined && /^(?:pid|groupId|sessionId|processId)$/i.test(key);
}

function keyIsTimestamp(key: string | undefined): boolean {
  return key !== undefined && /(?:At|Time|Date|Expires|Started|Renewed|Created|Updated|Until)$/i.test(key);
}

function keyIsPort(key: string | undefined): boolean {
  return key !== undefined && /^(?:port|portNumber)$/i.test(key);
}

function keyIsPreparationDuration(key: string | undefined): boolean {
  return key !== undefined && (/^(?:total|last|average)PreparationMs$/i.test(key) || key === "leaseDurationMinutes");
}

function keyIsFingerprint(key: string | undefined): boolean {
  return key !== undefined && /^(?:fingerprint|preparedFingerprint|projectionFingerprint)$/i.test(key);
}

function normalizeValue(value: unknown, context: NormalizationContext, key?: string): unknown {
  if (typeof value === "string") {
    if (keyIsTimestamp(key)) return "<timestamp>";
    if (keyIsPID(key)) return "<pid>";
    if (keyIsFingerprint(key)) return "<fingerprint>";
    return normalizeText(value, context);
  }
  if (typeof value === "number" && keyIsPreparationDuration(key)) return "<duration>";
  if (typeof value === "number" && keyIsPort(key)) return "<port>";
  if (typeof value === "number" && keyIsPID(key)) return "<pid>";
  if (Array.isArray(value)) return value.map((entry) => normalizeValue(entry, context));
  if (value && typeof value === "object") {
    if (key === "trees" || key === "workspaces") {
      return Object.values(value)
        .map((entry) => normalizeValue(entry, context))
        .sort((left, right) => JSON.stringify(left).localeCompare(JSON.stringify(right)));
    }
    return Object.fromEntries(
      Object.entries(value)
        .sort(([left], [right]) => left.localeCompare(right))
        .map(([entryKey, entryValue]) => [
          entryKey,
          key?.toLowerCase() === "ports" && typeof entryValue === "number"
            ? "<port>"
            : normalizeValue(entryValue, context, entryKey),
        ]),
    );
  }
  return value;
}

export function normalizeJSON(value: unknown, context: NormalizationContext = {}): unknown {
  return normalizeValue(value, context);
}

export function canonicalJSON(value: unknown, context: NormalizationContext = {}): string {
  return JSON.stringify(normalizeJSON(value, context)) ?? "null";
}

export function parseJSON(value: string): unknown | null {
  const trimmed = value.trim();
  if (!trimmed) return null;
  try {
    return JSON.parse(trimmed) as unknown;
  } catch {
    return null;
  }
}

export function normalizeRepositoryPath(repository: string): string {
  return path.resolve(repository).replaceAll("\\", "/");
}
