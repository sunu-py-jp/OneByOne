export interface DiffLine {
  text: string;
  type: "meta" | "hunk" | "added" | "removed" | "context";
  oldLine: number | null;
  newLine: number | null;
}

export function parseUnifiedDiff(content: string): DiffLine[] {
  if (!content) return [];

  const lines = content.split(/\r?\n/);
  if (lines[lines.length - 1] === "") lines.pop();

  let oldLine = 0;
  let newLine = 0;
  let oldRemaining = 0;
  let newRemaining = 0;

  return lines.map((text): DiffLine => {
    const line: DiffLine = { text, type: "meta", oldLine: null, newLine: null };
    const hunk = /^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@(?:.*)$/.exec(text);
    if (hunk) {
      oldLine = Number(hunk[1]);
      newLine = Number(hunk[3]);
      oldRemaining = Number(hunk[2] ?? 1);
      newRemaining = Number(hunk[4] ?? 1);
      line.type = "hunk";
      return line;
    }

    // This marker can occur between a removed line and its replacement.
    if (text.startsWith("\\ No newline at end of file")) return line;

    // Use the hunk's counts so file headers and trailing metadata never get
    // source numbers. Inside a hunk, --- and +++ can be ordinary source text.
    if (text.startsWith("-") && oldRemaining > 0) {
      line.type = "removed";
      line.oldLine = oldLine++;
      oldRemaining--;
    } else if (text.startsWith("+") && newRemaining > 0) {
      line.type = "added";
      line.newLine = newLine++;
      newRemaining--;
    } else if (text.startsWith(" ") && oldRemaining > 0 && newRemaining > 0) {
      line.type = "context";
      line.oldLine = oldLine++;
      line.newLine = newLine++;
      oldRemaining--;
      newRemaining--;
    } else {
      oldRemaining = 0;
      newRemaining = 0;
    }
    return line;
  });
}
