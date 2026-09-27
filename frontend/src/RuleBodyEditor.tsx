import { useId, useState } from "react";
import { RuleMarkdown } from "./RuleMarkdown";

export function RuleBodyEditor({ body, readOnly = false, onChange }: {
  body: string;
  readOnly?: boolean;
  onChange: (body: string) => void;
}) {
  const [mode, setMode] = useState<"source" | "preview">("preview");
  const id = useId();
  return <section className="rule-body-editor" aria-label="ルール本文">
    <div className="rule-body-heading">
      <span className="field-label" id={`${id}-label`}>本文</span>
      <div className="segmented" role="group" aria-label="本文の表示形式">
        <button type="button" className={mode === "source" ? "active" : ""} aria-label="原文" aria-pressed={mode === "source"} aria-controls={`${id}-content`} onClick={() => setMode("source")}>{"</>"}</button>
        <button type="button" className={mode === "preview" ? "active" : ""} aria-pressed={mode === "preview"} aria-controls={`${id}-content`} onClick={() => setMode("preview")}>Preview</button>
      </div>
    </div>
    <div id={`${id}-content`} className="rule-body-content">
      {mode === "source" ? <textarea className="code-input" aria-labelledby={`${id}-label`} value={body} rows={14} readOnly={readOnly} onChange={event => onChange(event.target.value)} spellCheck={false} placeholder="# 変更概要" />
        : <div className="rule-body-preview"><RuleMarkdown body={body} /></div>}
    </div>
  </section>;
}
