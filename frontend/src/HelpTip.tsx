import {
  useCallback,
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { createPortal } from "react-dom";
import "./help-tip.css";

type Position = {
  left: number;
  top: number;
  arrowLeft: number;
  maxHeight: number;
  placement: "above" | "below";
};

let activeTip: { id: string; close: () => void } | undefined;

export function HelpTip({
  label,
  children,
}: {
  label: string;
  children: ReactNode;
}) {
  const id = useId();
  const trigger = useRef<HTMLButtonElement>(null);
  const bubble = useRef<HTMLDivElement>(null);
  const content = useRef<HTMLDivElement>(null);
  const closeTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const hovered = useRef(false);
  const bubbleHovered = useRef(false);
  const focused = useRef(false);
  const pinned = useRef(false);
  const dismissed = useRef(false);
  const [open, setOpen] = useState(false);
  const [position, setPosition] = useState<Position | null>(null);

  const clearCloseTimer = useCallback(() => {
    clearTimeout(closeTimer.current);
    closeTimer.current = undefined;
  }, []);

  const close = useCallback(() => {
    if (activeTip?.id === id) activeTip = undefined;
    clearCloseTimer();
    pinned.current = false;
    dismissed.current = true;
    bubbleHovered.current = false;
    setOpen(false);
    setPosition(null);
  }, [clearCloseTimer, id]);

  const show = () => {
    if (activeTip?.id !== id) activeTip?.close();
    activeTip = { id, close };
    clearCloseTimer();
    dismissed.current = false;
    setOpen(true);
  };

  const scheduleClose = () => {
    clearCloseTimer();
    closeTimer.current = setTimeout(() => {
      if (
        !pinned.current &&
        !hovered.current &&
        !bubbleHovered.current &&
        !focused.current
      ) {
        close();
      }
    }, 140);
  };

  useEffect(() => {
    return () => {
      clearCloseTimer();
      if (activeTip?.id === id) activeTip = undefined;
    };
  }, [clearCloseTimer, id]);

  useLayoutEffect(() => {
    if (!open) return;
    let frame = 0;
    const update = () => {
      if (!trigger.current || !bubble.current || !content.current) return;
      const anchor = trigger.current.getBoundingClientRect();
      const size = bubble.current.getBoundingClientRect();
      const margin = 12;
      const gap = 11;
      const width = document.documentElement.clientWidth;
      const height = window.innerHeight;
      if (
        anchor.bottom < 0 ||
        anchor.top > height ||
        anchor.right < 0 ||
        anchor.left > width
      ) {
        close();
        return;
      }
      const below = height - anchor.bottom - gap - margin;
      const above = anchor.top - gap - margin;
      // scrollHeight preserves the full content height after the popup is capped.
      // Measuring only its visible height can alternate the placement on resize.
      const naturalHeight = content.current.scrollHeight + 2;
      const placement = below >= naturalHeight || below >= above
        ? "below"
        : "above";
      const maxHeight = Math.max(40, placement === "below" ? below : above);
      const bubbleHeight = Math.min(naturalHeight, maxHeight);
      const anchorCenter = anchor.left + anchor.width / 2;
      const left = Math.max(
        margin,
        Math.min(anchorCenter - size.width / 2, width - size.width - margin),
      );
      const top = placement === "below"
        ? anchor.bottom + gap
        : anchor.top - gap - bubbleHeight;
      setPosition({
        left,
        top: Math.max(margin, top),
        arrowLeft: Math.max(16, Math.min(anchorCenter - left, size.width - 16)),
        placement,
        maxHeight,
      });
    };
    const scheduleUpdate = () => {
      cancelAnimationFrame(frame);
      frame = requestAnimationFrame(update);
    };
    update();
    window.addEventListener("resize", scheduleUpdate);
    window.addEventListener("scroll", scheduleUpdate, true);
    const observer = typeof ResizeObserver === "undefined"
      ? undefined
      : new ResizeObserver(scheduleUpdate);
    if (bubble.current) observer?.observe(bubble.current);
    if (trigger.current) observer?.observe(trigger.current);
    return () => {
      cancelAnimationFrame(frame);
      observer?.disconnect();
      window.removeEventListener("resize", scheduleUpdate);
      window.removeEventListener("scroll", scheduleUpdate, true);
    };
  }, [open, close, label, children]);

  useEffect(() => {
    if (!open) return;
    const onPointerDown = (event: PointerEvent) => {
      const target = event.target as Node;
      if (!trigger.current?.contains(target) && !bubble.current?.contains(target)) {
        close();
      }
    };
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        event.preventDefault();
        event.stopPropagation();
        close();
      }
    };
    document.addEventListener("pointerdown", onPointerDown, true);
    document.addEventListener("keydown", onKeyDown, true);
    return () => {
      document.removeEventListener("pointerdown", onPointerDown, true);
      document.removeEventListener("keydown", onKeyDown, true);
    };
  }, [open, close]);

  return (
    <>
      <button
        ref={trigger}
        type="button"
        className={`help-tip-trigger${open ? " is-open" : ""}`}
        aria-label={`${label}の補足`}
        aria-expanded={open}
        aria-controls={open ? id : undefined}
        aria-describedby={open ? id : undefined}
        onPointerEnter={(event) => {
          if (event.pointerType === "touch") return;
          hovered.current = true;
          show();
        }}
        onPointerLeave={() => {
          hovered.current = false;
          scheduleClose();
        }}
        onFocus={() => {
          focused.current = true;
          show();
        }}
        onBlur={() => {
          focused.current = false;
          scheduleClose();
        }}
        onClick={(event) => {
          event.preventDefault();
          event.stopPropagation();
          if (pinned.current) close();
          else {
            pinned.current = true;
            show();
          }
        }}
      >
        <svg width="17" height="17" viewBox="0 0 20 20" fill="none" aria-hidden="true">
          <circle cx="10" cy="10" r="7.4" stroke="currentColor" strokeWidth="1.35" />
          <path
            d="M7.9 7.5a2.1 2.1 0 0 1 4.2.2c0 1.6-2.1 1.7-2.1 3.2"
            stroke="currentColor"
            strokeWidth="1.45"
            strokeLinecap="round"
          />
          <circle cx="10" cy="13.4" r=".8" fill="currentColor" />
        </svg>
      </button>
      {open && createPortal(
        <div
          ref={bubble}
          id={id}
          role="tooltip"
          className="help-tip-bubble"
          data-placement={position?.placement ?? "below"}
          style={{
            left: position?.left ?? 0,
            top: position?.top ?? 0,
            visibility: position ? "visible" : "hidden",
          }}
          onPointerEnter={() => {
            bubbleHovered.current = true;
            clearCloseTimer();
            if (!dismissed.current) setOpen(true);
          }}
          onPointerLeave={() => {
            bubbleHovered.current = false;
            scheduleClose();
          }}
        >
          <span
            className="help-tip-arrow"
            style={{ left: position?.arrowLeft ?? 16 }}
            aria-hidden="true"
          />
          <div
            ref={content}
            className="help-tip-content"
            style={{ maxHeight: position ? position.maxHeight - 2 : undefined }}
          >
            <div className="help-tip-heading">{label}</div>
            <div className="help-tip-text">{children}</div>
          </div>
        </div>,
        document.body,
      )}
    </>
  );
}
