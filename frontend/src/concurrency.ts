import type { Config } from "./types";

// This setting changes scheduling, not the confirmed file/rule mapping.
export function onlyConcurrencyChanged(draft: Config, saved: Config): boolean {
  return [...new Set([...Object.keys(draft), ...Object.keys(saved)])].every(key =>
    key === "concurrency" || JSON.stringify(draft[key as keyof Config]) === JSON.stringify(saved[key as keyof Config]));
}
