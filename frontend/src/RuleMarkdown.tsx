import Markdown from "react-markdown";
import remarkGfm from "remark-gfm";
import type { MouseEvent } from "react";
import "./rule-markdown.css";

type MarkdownNode = { type: string; value?: string; children?: MarkdownNode[] };

/** Interpret only the report writer's cell separators, never arbitrary HTML. */
function remarkReportCells() {
  return (root: MarkdownNode) => {
    const walk = (node: MarkdownNode, inCell = false) => {
      const cell = inCell || node.type === "tableCell";
      if (cell && node.type === "html" && /^<br\s*\/?\s*>$/i.test(node.value || "")) {
        node.type = "break";
        delete node.value;
      }
      node.children?.forEach(child => walk(child, cell));
    };
    walk(root);
  };
}

/** Render author-provided Markdown without executing HTML or loading remote images. */
export function RuleMarkdown({ body, onLinkClick, reportCells = false }: { body: string; onLinkClick?: (href: string, event: MouseEvent<HTMLAnchorElement>) => void; reportCells?: boolean }) {
  return <div className="rule-markdown"><Markdown remarkPlugins={reportCells ? [remarkGfm, remarkReportCells] : [remarkGfm]} components={{
    a: ({ children, href }) => <a href={href} target="_blank" rel="noopener noreferrer" onClick={onLinkClick ? event => onLinkClick(href || "", event) : undefined}>{children}</a>,
    img: ({ alt }) => <span className="rule-markdown-image">{alt || "画像"}</span>,
  }}>{body || "本文はまだ入力されていません。"}</Markdown></div>;
}
