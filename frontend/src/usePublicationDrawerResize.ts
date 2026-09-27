import { useCallback, useEffect, useRef, useState, type KeyboardEvent, type PointerEvent } from "react";

export function publicationDrawerBounds(container: number) {
  const max = Math.max(0, container - (container > 640 ? 40 : 0));
  return { min: Math.min(320, max), max };
}
export function clampPublicationDrawerWidth(width: number, container: number) {
  const { min, max } = publicationDrawerBounds(container);
  return Math.round(Math.max(min, Math.min(max, width)));
}

export function usePublicationDrawerResize(workspaceId: string) {
  const [element, setElement] = useState<HTMLElement | null>(null);
  const [container, setContainer] = useState(0);
  const [preferred, setPreferred] = useState<number | null>(null);
  const [resizing, setResizing] = useState(false);
  const drag = useRef<{ pointerId: number; x: number; width: number; previous: number | null; handle: HTMLDivElement } | null>(null);
  const width = clampPublicationDrawerWidth(preferred ?? container * .66, container);
  const bounds = publicationDrawerBounds(container);
  const finish = useCallback((cancel: boolean) => {
    const current = drag.current;
    if (!current) return;
    drag.current = null;
    if (cancel) setPreferred(current.previous);
    setResizing(false);
    if (current.handle.hasPointerCapture(current.pointerId)) current.handle.releasePointerCapture(current.pointerId);
  }, []);
  useEffect(() => { finish(true); setPreferred(null); }, [workspaceId, finish]);
  useEffect(() => {
    if (!element) return;
    const measure = () => { finish(true); setContainer(element.clientWidth); };
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(element);
    return () => observer.disconnect();
  }, [element, finish]);
  useEffect(() => {
    if (!resizing) return;
    const cancel = () => finish(true);
    document.body.classList.add("results-resizing");
    window.addEventListener("blur", cancel);
    return () => { document.body.classList.remove("results-resizing"); window.removeEventListener("blur", cancel); };
  }, [resizing, finish]);
  useEffect(() => () => {
    const current = drag.current;
    drag.current = null;
    if (current?.handle.hasPointerCapture(current.pointerId)) current.handle.releasePointerCapture(current.pointerId);
  }, []);
  function onPointerDown(event: PointerEvent<HTMLDivElement>) {
    if (!event.isPrimary || event.button !== 0 || drag.current) return;
    event.preventDefault(); event.currentTarget.focus(); event.currentTarget.setPointerCapture(event.pointerId);
    drag.current = { pointerId: event.pointerId, x: event.clientX, width, previous: preferred, handle: event.currentTarget };
    setResizing(true);
  }
  function onPointerMove(event: PointerEvent<HTMLDivElement>) {
    if (drag.current?.pointerId !== event.pointerId) return;
    setPreferred(clampPublicationDrawerWidth(drag.current.width + drag.current.x - event.clientX, container));
  }
  function onKeyDown(event: KeyboardEvent<HTMLDivElement>) {
    if (event.key === "Escape" && drag.current) { event.preventDefault(); event.stopPropagation(); finish(true); return; }
    let next: number | null;
    if (event.key === "ArrowLeft") next = width + (event.shiftKey ? 40 : 10);
    else if (event.key === "ArrowRight") next = width - (event.shiftKey ? 40 : 10);
    else if (event.key === "Home") next = bounds.min;
    else if (event.key === "End") next = bounds.max;
    else if (event.key === "Enter") next = null;
    else return;
    event.preventDefault(); finish(false);
    setPreferred(next === null ? null : clampPublicationDrawerWidth(next, container));
  }
  return { ref: setElement, width, bounds, ready: container > 0, resizing, finish,
    handleProps: { onPointerDown, onPointerMove, onKeyDown,
      onPointerUp: (event: PointerEvent<HTMLDivElement>) => { if (drag.current?.pointerId === event.pointerId) finish(false); },
      onPointerCancel: () => finish(true), onLostPointerCapture: () => finish(true),
      onDoubleClick: () => { finish(false); setPreferred(null); },
    },
  };
}
