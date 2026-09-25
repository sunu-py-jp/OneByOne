import { useCallback, useEffect, useId, useLayoutEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { Icon } from "./icons";
import type { WorkspaceIssue } from "./types";
import "./workspace-issues.css";

type Position = {
  left: number;
  top: number;
  arrowLeft: number;
  maxHeight: number;
  placement: "above" | "below";
};

let activeIssues: { id: string; close: () => void } | undefined;

export function WorkspaceIssues({
  workspaceName,
  issues,
  disabled,
  onSelect,
}: {
  workspaceName: string;
  issues: WorkspaceIssue[];
  disabled?: boolean;
  onSelect: (issue: WorkspaceIssue) => void;
}) {
  const id = useId();
  const trigger = useRef<HTMLButtonElement>(null);
  const bubble = useRef<HTMLDivElement>(null);
  const content = useRef<HTMLDivElement>(null);
  const closeTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const hovered = useRef(false);
  const bubbleHovered = useRef(false);
  const pinned = useRef(false);
  const [open, setOpen] = useState(false);
  const [position, setPosition] = useState<Position | null>(null);

  const clearCloseTimer = useCallback(() => {
    clearTimeout(closeTimer.current);
    closeTimer.current = undefined;
  }, []);

  const close = useCallback(() => {
    clearCloseTimer();
    if (activeIssues?.id === id) activeIssues = undefined;
    pinned.current = false;
    bubbleHovered.current = false;
    setOpen(false);
    setPosition(null);
  }, [clearCloseTimer, id]);

  const show = () => {
    if (activeIssues?.id !== id) activeIssues?.close();
    activeIssues = { id, close };
    clearCloseTimer();
    setOpen(true);
  };

  const scheduleClose = () => {
    clearCloseTimer();
    closeTimer.current = setTimeout(() => {
      const focused = trigger.current === document.activeElement ||
        bubble.current?.contains(document.activeElement);
      if (!pinned.current && !hovered.current && !bubbleHovered.current && !focused) close();
    }, 160);
  };

  useEffect(() => {
    if (issues.length === 0) close();
  }, [issues.length, close]);

  useEffect(() => () => {
    clearCloseTimer();
    if (activeIssues?.id === id) activeIssues = undefined;
  }, [clearCloseTimer, id]);

  useLayoutEffect(() => {
    if (!open) return;
    let frame = 0;
    const update = () => {
      if (!trigger.current || !bubble.current || !content.current) return;
      const anchor = trigger.current.getBoundingClientRect();
      const size = bubble.current.getBoundingClientRect();
      const width = document.documentElement.clientWidth;
      const height = window.innerHeight;
      const margin = 12;
      const gap = 11;
      if (anchor.bottom < 0 || anchor.top > height || anchor.right < 0 || anchor.left > width) {
        close();
        return;
      }
      const below = height - anchor.bottom - gap - margin;
      const above = anchor.top - gap - margin;
      const naturalHeight = content.current.scrollHeight + 2;
      const placement = below >= Math.min(naturalHeight, 420) || below >= above ? "below" : "above";
      const maxHeight = Math.max(0, Math.min(420, placement === "below" ? below : above));
      const anchorCenter = anchor.left + anchor.width / 2;
      const left = Math.max(margin, Math.min(anchorCenter - size.width / 2, width - size.width - margin));
      const top = placement === "below" ? anchor.bottom + gap : anchor.top - gap - Math.min(naturalHeight, maxHeight);
      setPosition({
        left,
        top: Math.max(margin, top),
        arrowLeft: Math.max(16, Math.min(anchorCenter - left, size.width - 16)),
        maxHeight,
        placement,
      });
    };
    const scheduleUpdate = () => {
      cancelAnimationFrame(frame);
      frame = requestAnimationFrame(update);
    };
    update();
    window.addEventListener("resize", scheduleUpdate);
    window.addEventListener("scroll", scheduleUpdate, true);
    const observer = typeof ResizeObserver === "undefined" ? undefined : new ResizeObserver(scheduleUpdate);
    if (bubble.current) observer?.observe(bubble.current);
    if (trigger.current) observer?.observe(trigger.current);
    return () => {
      cancelAnimationFrame(frame);
      observer?.disconnect();
      window.removeEventListener("resize", scheduleUpdate);
      window.removeEventListener("scroll", scheduleUpdate, true);
    };
  }, [open, close, issues, workspaceName]);

  useEffect(() => {
    if (!open) return;
    const onPointerDown = (event: PointerEvent) => {
      const target = event.target as Node;
      if (!trigger.current?.contains(target) && !bubble.current?.contains(target)) close();
    };
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key !== "Escape") return;
      event.preventDefault();
      event.stopPropagation();
      if (bubble.current?.contains(document.activeElement)) trigger.current?.focus();
      close();
    };
    document.addEventListener("pointerdown", onPointerDown, true);
    document.addEventListener("keydown", onKeyDown, true);
    return () => {
      document.removeEventListener("pointerdown", onPointerDown, true);
      document.removeEventListener("keydown", onKeyDown, true);
    };
  }, [open, close]);

  if (issues.length === 0) return null;

  return <>
    <button
      ref={trigger}
      type="button"
      className={`workspace-issues-trigger${open ? " is-open" : ""}`}
      aria-label={`${workspaceName}のエラー ${issues.length}件`}
      aria-haspopup="dialog"
      aria-expanded={open}
      aria-controls={open ? id : undefined}
      onPointerEnter={(event) => {
        if (event.pointerType === "touch") return;
        hovered.current = true;
        show();
      }}
      onPointerLeave={() => {
        hovered.current = false;
        scheduleClose();
      }}
      onFocus={show}
      onBlur={scheduleClose}
      onKeyDown={(event) => {
        if (event.key !== "Tab" || !open) return;
        if (event.shiftKey) close();
        else {
          event.preventDefault();
          bubble.current?.querySelector<HTMLButtonElement>("button")?.focus();
        }
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
      <Icon name="warning" size={17} />
    </button>
    {open && createPortal(
      <div
        ref={bubble}
        id={id}
        role="dialog"
        aria-labelledby={`${id}-heading`}
        className="workspace-issues-bubble"
        data-placement={position?.placement ?? "below"}
        style={{ left: position?.left ?? 0, top: position?.top ?? 0, visibility: position ? "visible" : "hidden" }}
        onPointerEnter={() => {
          bubbleHovered.current = true;
          clearCloseTimer();
        }}
        onPointerLeave={() => {
          bubbleHovered.current = false;
          scheduleClose();
        }}
        onFocus={clearCloseTimer}
        onBlur={scheduleClose}
        onKeyDown={(event) => {
          if (event.key !== "Tab") return;
          const buttons = bubble.current?.querySelectorAll<HTMLButtonElement>("button");
          if (!buttons?.length) return;
          if (event.shiftKey && event.target === buttons[0]) {
            event.preventDefault();
            trigger.current?.focus();
          } else if (!event.shiftKey && event.target === buttons[buttons.length - 1]) {
            // The portal lives at the end of body; continue from the sidebar trigger.
            const focusable = [...document.querySelectorAll<HTMLElement>(
              'button, a[href], input, select, textarea, [tabindex]',
            )].filter((element) => element.tabIndex >= 0 && !element.matches(":disabled") &&
              element.getClientRects().length > 0 && !bubble.current?.contains(element));
            const next = focusable[focusable.indexOf(trigger.current!) + 1];
            if (next) {
              event.preventDefault();
              next.focus();
            }
            close();
          }
        }}
      >
        <span className="workspace-issues-arrow" style={{ left: position?.arrowLeft ?? 16 }} aria-hidden="true" />
        <div ref={content} className="workspace-issues-content" style={{ maxHeight: position ? Math.max(0, position.maxHeight - 2) : undefined }}>
          <div id={`${id}-heading`} className="workspace-issues-heading">{workspaceName}のエラー <span>{issues.length}件</span></div>
          <ul className="workspace-issues-list">
            {issues.map((issue) => <li key={issue.id}>
              <button
                type="button"
                className="workspace-issues-item"
                aria-disabled={disabled || undefined}
                onClick={(event) => {
                  event.stopPropagation();
                  if (disabled) return;
                  close();
                  onSelect(issue);
                }}
              >
                <span>{issue.message}</span>
                <Icon name="chevron" size={14} />
              </button>
            </li>)}
          </ul>
        </div>
      </div>,
      document.body,
    )}
  </>;
}
