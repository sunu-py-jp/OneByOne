import { useCallback, useEffect, useLayoutEffect, useRef, useState, type KeyboardEvent, type PointerEvent } from "react";
import { clampResultListWidth, fitResultListWidth, resultSplitBounds } from "./results-splitter";

interface Drag {
  pointerId: number;
  startX: number;
  startWidth: number;
  previous: number | null;
  handle: HTMLDivElement;
}

export function useResultsSplitter(workspaceID: string, files: string[], { extraWidth = 210, sampleSelector = ".results-file-link > span" }: { extraWidth?: number; sampleSelector?: string } = {}) {
  const [element, setElement] = useState<HTMLDivElement | null>(null);
  const [measurement, setMeasurement] = useState({ container: 0, content: 340 });
  const [preferredWidth, setPreferredWidth] = useState<number | null>(null);
  const [resizing, setResizing] = useState(false);
  const drag = useRef<Drag | null>(null);
  const fileKey = files.join("\0");
  const fittedWidth = fitResultListWidth(measurement.content, measurement.container);
  const width = clampResultListWidth(preferredWidth ?? fittedWidth, measurement.container);
  const bounds = resultSplitBounds(measurement.container);

  const finish = useCallback((cancel: boolean) => {
    const active = drag.current;
    if (!active) return;
    drag.current = null;
    if (cancel) setPreferredWidth(active.previous);
    setResizing(false);
    if (active.handle.hasPointerCapture(active.pointerId)) active.handle.releasePointerCapture(active.pointerId);
  }, []);

  useEffect(() => { finish(true); setPreferredWidth(null); }, [workspaceID, finish]);

  useLayoutEffect(() => {
    if (!element) return;
    let disposed = false;
    let measuredFont = "";
    let textWidth = 0;
    const context = document.createElement("canvas").getContext("2d");
    const measure = () => {
      if (disposed) return;
      const sample = element.querySelector(sampleSelector) || element;
      const font = getComputedStyle(sample).font;
      // Status polling keeps fileKey stable; resizing also reuses measured glyph widths.
      if (!measuredFont || measuredFont !== font) {
        measuredFont = font;
        if (context) context.font = font;
        textWidth = files.reduce((max, file) => Math.max(max, context?.measureText(file).width ?? file.length * 6), 0);
      }
      setMeasurement(previous => {
        const next = { container: element.clientWidth, content: Math.ceil(textWidth) + extraWidth };
        return previous.container === next.container && previous.content === next.content ? previous : next;
      });
    };
    measure();
    const observer = new ResizeObserver(() => { finish(true); measure(); });
    observer.observe(element);
    void document.fonts?.ready.then(() => { measuredFont = ""; measure(); });
    return () => { disposed = true; observer.disconnect(); };
  }, [element, fileKey, finish, extraWidth, sampleSelector]);

  useEffect(() => {
    if (!resizing) return;
    const cancel = () => finish(true);
    const key = (event: globalThis.KeyboardEvent) => { if (event.key === "Escape") { event.preventDefault(); cancel(); } };
    const visibility = () => { if (document.hidden) cancel(); };
    document.body.classList.add("results-resizing");
    window.addEventListener("blur", cancel);
    window.addEventListener("resize", cancel);
    window.addEventListener("keydown", key);
    document.addEventListener("visibilitychange", visibility);
    return () => {
      document.body.classList.remove("results-resizing");
      window.removeEventListener("blur", cancel);
      window.removeEventListener("resize", cancel);
      window.removeEventListener("keydown", key);
      document.removeEventListener("visibilitychange", visibility);
    };
  }, [resizing, finish]);

  useEffect(() => () => {
    const active = drag.current;
    drag.current = null;
    if (active?.handle.hasPointerCapture(active.pointerId)) active.handle.releasePointerCapture(active.pointerId);
  }, []);

  function onPointerDown(event: PointerEvent<HTMLDivElement>) {
    if (!event.isPrimary || event.button !== 0 || drag.current) return;
    event.preventDefault();
    event.currentTarget.focus();
    event.currentTarget.setPointerCapture(event.pointerId);
    drag.current = { pointerId: event.pointerId, startX: event.clientX, startWidth: width, previous: preferredWidth, handle: event.currentTarget };
    setResizing(true);
  }
  function onPointerMove(event: PointerEvent<HTMLDivElement>) {
    const active = drag.current;
    if (!active || active.pointerId !== event.pointerId) return;
    setPreferredWidth(clampResultListWidth(active.startWidth + event.clientX - active.startX, measurement.container));
  }
  function onKeyDown(event: KeyboardEvent<HTMLDivElement>) {
    let next: number | null;
    if (event.key === "ArrowLeft") next = width - (event.shiftKey ? 40 : 10);
    else if (event.key === "ArrowRight") next = width + (event.shiftKey ? 40 : 10);
    else if (event.key === "Home") next = bounds.min;
    else if (event.key === "End") next = bounds.max;
    else if (event.key === "Enter") next = null;
    else return;
    event.preventDefault();
    finish(false);
    setPreferredWidth(next === null ? null : clampResultListWidth(next, measurement.container));
  }

  return {
    ref: setElement, width, ready: measurement.container > 0, resizing, bounds,
    handleProps: {
      onPointerDown, onPointerMove, onKeyDown,
      onPointerUp: (event: PointerEvent<HTMLDivElement>) => { if (drag.current?.pointerId === event.pointerId) finish(false); },
      onPointerCancel: () => finish(true), onLostPointerCapture: () => finish(true),
      onDoubleClick: () => { finish(false); setPreferredWidth(null); },
    },
  };
}
