import Markdown from "react-markdown";
import remarkGfm from "remark-gfm";
import type { MouseEvent } from "react";
import "./rule-markdown.css";

/** Render author-provided Markdown without executing HTML or loading remote images. */
export function RuleMarkdown({ body, onLinkClick }: { body: string; onLinkClick?: (href: string, event: MouseEvent<HTMLAnchorElement>) => void }) {
  return <div className="rule-markdown"><Markdown remarkPlugins={[remarkGfm]} components={{
    a: ({ children, href }) => <a href={href} target="_blank" rel="noopener noreferrer" onClick={onLinkClick ? event => onLinkClick(href || "", event) : undefined}>{children}</a>,
    img: ({ alt }) => <span className="rule-markdown-image">{alt || "画像"}</span>,
  }}>{body || "本文はまだ入力されていません。"}</Markdown></div>;
}
