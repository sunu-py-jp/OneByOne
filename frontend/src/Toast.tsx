import { useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { Icon } from "./icons";
import "./toast.css";

type ToastNotice = {
  type: "success" | "error" | "info";
  message: string;
};

type ToastProps = {
  notice: ToastNotice | null;
  onDismiss: () => void;
};

export function Toast({ notice, onDismiss }: ToastProps) {
  if (!notice) return null;
  return createPortal(
    <div className="toast-region">
      <ToastMessage key={`${notice.type}:${notice.message}`} notice={notice} onDismiss={onDismiss} />
    </div>,
    document.body,
  );
}

function ToastMessage({ notice, onDismiss }: { notice: ToastNotice; onDismiss: () => void }) {
  const [hovered, setHovered] = useState(false);
  const [focused, setFocused] = useState(false);
  const remaining = useRef(notice.type === "error" ? 8000 : 5000);
  const previousNotice = useRef(notice);
  const dismiss = useRef(onDismiss);
  const paused = hovered || focused;

  useEffect(() => {
    dismiss.current = onDismiss;
  }, [onDismiss]);

  useEffect(() => {
    if (previousNotice.current !== notice) {
      previousNotice.current = notice;
      remaining.current = notice.type === "error" ? 8000 : 5000;
    }
    if (paused) return;
    const started = performance.now();
    const timer = window.setTimeout(() => dismiss.current(), remaining.current);
    return () => {
      window.clearTimeout(timer);
      remaining.current = Math.max(0, remaining.current - (performance.now() - started));
    };
  }, [notice, paused]);

  return (
    <div
      className={`toast toast-${notice.type}`}
      onMouseEnter={() => setHovered(true)}
      onMouseLeave={() => setHovered(false)}
      onFocusCapture={() => setFocused(true)}
      onBlurCapture={(event) => {
        if (!event.currentTarget.contains(event.relatedTarget)) setFocused(false);
      }}
    >
      <span className="toast-icon"><Icon name={notice.type === "error" ? "warning" : notice.type === "success" ? "check" : "info"} size={18} /></span>
      <p className="toast-message" role={notice.type === "error" ? "alert" : "status"} aria-atomic="true">{notice.message}</p>
      <button type="button" className="toast-dismiss" aria-label="通知を閉じる" title="通知を閉じる" onClick={onDismiss}>
        <Icon name="close" size={16} />
      </button>
    </div>
  );
}
