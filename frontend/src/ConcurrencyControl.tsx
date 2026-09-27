import { useId } from "react";
import { normalizeConcurrency } from "./types";

export function ConcurrencyControl({ value, disabled, onChange }: {
  value: number; disabled: boolean; onChange(value: number): void;
}) {
  const id = useId();
  const count = normalizeConcurrency(value);
  return <div className="concurrency-control">
    <label htmlFor={id}>並列数</label>
    <div className="concurrency-stepper">
      <button type="button" aria-label="並列数を減らす" disabled={disabled || count <= 1} onClick={() => onChange(count - 1)}>−</button>
      <input id={id} type="number" min={1} max={10} step={1} value={count} disabled={disabled}
        onChange={event => { if (Number.isFinite(event.currentTarget.valueAsNumber)) onChange(Math.min(10, Math.max(1, Math.trunc(event.currentTarget.valueAsNumber)))); }} />
      <button type="button" aria-label="並列数を増やす" disabled={disabled || count >= 10} onClick={() => onChange(count + 1)}>+</button>
    </div>
  </div>;
}
