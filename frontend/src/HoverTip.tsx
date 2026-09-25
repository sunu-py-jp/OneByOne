import { useEffect, useId, useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";
import "./hover-tip.css";

// Disabled native controls do not receive pointer/focus events. Their wrapper
// exposes the reason without enabling the underlying action.
export function HoverTip({ reason, children, className = "" }: { reason?: string; children: ReactNode; className?: string }) {
  const id = useId();
  const anchor = useRef<HTMLSpanElement>(null);
  const bubble = useRef<HTMLDivElement>(null);
  const [open, setOpen] = useState(false);
  const [position, setPosition] = useState<{ left: number; top: number; arrow: number; below: boolean } | null>(null);
  useLayoutEffect(() => {
    if (!open || !reason) return;
    const update = () => {
      if (!anchor.current || !bubble.current) return;
      const rect = anchor.current.getBoundingClientRect();
      const box = bubble.current.getBoundingClientRect();
      const left = Math.max(10, Math.min(rect.left + rect.width / 2 - box.width / 2, window.innerWidth - box.width - 10));
      const below = rect.top < box.height + 20;
      const top = below ? rect.bottom + 9 : rect.top - box.height - 9;
      setPosition({ left, top: Math.max(10, Math.min(top, window.innerHeight - box.height - 10)), arrow: Math.max(12, Math.min(rect.left + rect.width / 2 - left, box.width - 12)), below });
    };
    update();
    window.addEventListener("resize", update);
    window.addEventListener("scroll", update, true);
    return () => { window.removeEventListener("resize", update); window.removeEventListener("scroll", update, true); };
  }, [open, reason]);
  useEffect(() => {
    const close = (event: KeyboardEvent) => { if (event.key === "Escape") setOpen(false); };
    if (open) window.addEventListener("keydown", close);
    return () => window.removeEventListener("keydown", close);
  }, [open]);
  if (!reason) return <>{children}</>;
  return <>
    <span ref={anchor} className={`disabled-reason-trigger ${className}`} tabIndex={0} aria-describedby={open ? id : undefined} aria-label={reason}
      onPointerEnter={() => setOpen(true)} onPointerLeave={() => setOpen(false)} onFocus={() => setOpen(true)} onBlur={() => setOpen(false)}>
      {children}
    </span>
    {open && createPortal(<div ref={bubble} id={id} role="tooltip" className="disabled-reason-bubble" data-below={position?.below}
      style={{ left: position?.left || 0, top: position?.top || 0, visibility: position ? "visible" : "hidden" }}>
      <span className="disabled-reason-arrow" style={{ left: position?.arrow || 12 }} aria-hidden="true" />{reason}
    </div>, document.body)}
  </>;
}
