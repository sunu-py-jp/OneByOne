import { useEffect, useRef, useState } from "react";

interface Snapshot<T> {
  key: string;
  data?: T;
  error?: string;
  loading: boolean;
}

/** Refresh a selected result without replacing its existing view while I/O runs. */
export function useResultSnapshot<T>({ key, revision, enabled, poll, read }: {
  key: string;
  revision: string;
  enabled: boolean;
  poll: boolean;
  read: () => Promise<T>;
}) {
  const [snapshot, setSnapshot] = useState<Snapshot<T>>();
  const latest = useRef({ key, revision, poll, read });
  latest.current = { key, revision, poll, read };
  const refresh = useRef<(() => void) | null>(null);

  useEffect(() => {
    if (!enabled) return;
    let canceled = false;
    let running = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    async function fetch() {
      clearTimeout(timer);
      if (canceled || running) return;
      running = true;
      const request = latest.current;
      setSnapshot(previous => ({ key, data: previous?.key === key ? previous.data : undefined, loading: true }));
      try {
        const data = await request.read();
        // Reads of this key are sequential: a completed response is newer than
        // the last displayed one, even if another revision arrived meanwhile.
        // Keep making visible progress instead of discarding every slow read.
        if (!canceled && latest.current.key === key) setSnapshot({ key, data, loading: false });
      } catch (error) {
        if (!canceled && latest.current.key === key) setSnapshot(previous => ({
          key, data: previous?.key === key ? previous.data : undefined, loading: false,
          error: error instanceof Error ? error.message : String(error),
        }));
      } finally {
        running = false;
        if (!canceled && latest.current.key === key) {
          // Changes arriving during a slow read are coalesced into one fresh
          // read, without overlapping requests or stalling visible progress.
          if (latest.current.revision !== request.revision) void fetch();
          else if (latest.current.poll) timer = setTimeout(() => { void fetch(); }, 1000);
        }
      }
    }
    const requestRefresh = () => { void fetch(); };
    refresh.current = requestRefresh;
    return () => {
      canceled = true; clearTimeout(timer);
      if (refresh.current === requestRefresh) refresh.current = null;
    };
  }, [key, enabled]);
  useEffect(() => { if (enabled) refresh.current?.(); }, [key, revision, enabled, poll]);

  return snapshot?.key === key && enabled ? snapshot : { key, loading: enabled };
}
