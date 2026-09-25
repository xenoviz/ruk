export interface ChangelogEntry {
  readonly version: string;
  readonly date: string;
  readonly body: string;
}

const HEADING = /^## (\S+) - (\d{4}-\d{2}-\d{2})\s*$/;

/**
 * Return the dated changelog entry for version. Releases must document what
 * changed before they are tagged, so a missing, undated, or empty entry is an
 * error rather than an empty release body.
 */
export function changelogEntry(markdown: string, version: string): ChangelogEntry {
  const lines = markdown.replaceAll("\r\n", "\n").split("\n");
  const start = lines.findIndex((line) => HEADING.exec(line)?.[1] === version);
  if (start < 0) {
    throw new Error(`CHANGELOG.md has no dated "## ${version} - YYYY-MM-DD" entry`);
  }
  const date = HEADING.exec(lines[start]!)![2]!;
  let end = lines.findIndex((line, index) => index > start && line.startsWith("## "));
  if (end < 0) end = lines.length;
  const body = lines.slice(start + 1, end).join("\n").trim();
  if (body === "") throw new Error(`CHANGELOG.md entry for ${version} is empty`);
  return { version, date, body };
}
