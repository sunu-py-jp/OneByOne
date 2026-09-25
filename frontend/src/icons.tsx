import type { CSSProperties } from "react";

const paths = {
  more: "M5 12h.01M12 12h.01M19 12h.01",
  play: "M8 5v14l11-7-11-7Z",
  pause: "M8 5v14M16 5v14",
  stop: "M6 6h12v12H6z",
  queue: "M8 6h12M8 12h12M8 18h12M3 6h.01M3 12h.01M3 18h.01",
  rules:
    "M8 3H5a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2V5a2 2 0 0 0-2-2h-3M9 3h6v4H9V3ZM8 12h8M8 16h6",
  settings: "M4 7h16M4 17h16M8 4v6M16 14v6",
  folder:
    "M3 7a2 2 0 0 1 2-2h5l2 2h7a2 2 0 0 1 2 2v9a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V7Z",
  search: "m21 21-5-5M18 10a8 8 0 1 1-16 0 8 8 0 0 1 16 0Z",
  chevron: "m9 5 7 7-7 7",
  down: "m6 9 6 6 6-6",
  check: "m5 12 4 4L19 6",
  close: "m6 6 12 12M6 18 18 6",
  refresh:
    "M20 7v5h-5M4 17v-5h5M6.1 6a8 8 0 0 1 13.3 3M4.6 15a8 8 0 0 0 13.3 3",
  undo: "m9 4-5 5 5 5M4 9h10a6 6 0 0 1 0 12h-3",
  file: "M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8l-6-6ZM14 2v6h6M8 13h8M8 17h5",
  download: "M12 3v12m-5-5 5 5 5-5M4 16v4h16v-4",
  upload: "M12 16V4m-5 5 5-5 5 5M4 16v4h16v-4",
  terminal: "m4 6 6 6-6 6M13 18h7",
  shield: "M12 3 3 7v5c0 5 9 9 9 9s9-4 9-9V7l-9-4Zm-4 9 3 3 5-6",
  branch:
    "M6 3v12a4 4 0 0 0 8 0V9M14 9a3 3 0 1 0 0-6 3 3 0 0 0 0 6ZM6 21a3 3 0 1 0 0-6 3 3 0 0 0 0 6Z",
  info: "M12 11v6M12 7h.01M22 12a10 10 0 1 1-20 0 10 10 0 0 1 20 0Z",
  warning: "m12 3 10 18H2L12 3ZM12 9v5M12 17h.01",
  clock: "M12 6v6l4 2M22 12a10 10 0 1 1-20 0 10 10 0 0 1 20 0Z",
  link: "m10 13 4-4M8 16l-2 2a4 4 0 0 1-6-6l5-5a4 4 0 0 1 6 0m2 1 2-2a4 4 0 0 1 6 6l-5 5a4 4 0 0 1-6 0",
  spark: "m12 3 2.8 6.2L21 12l-6.2 2.8L12 21l-2.8-6.2L3 12l6.2-2.8L12 3Z",
  arrow: "M4 12h16m-6-6 6 6-6 6",
  plus: "M12 4v16M4 12h16",
  edit: "m16 3 5 5-12 12H4v-5L16 3Zm-3 3 5 5",
  trash: "M3 6h18M9 6V3h6v3M5 6l1 15h12l1-15M10 10v7M14 10v7",
  eye: "M2 12s4-7 10-7 10 7 10 7-4 7-10 7-10-7-10-7ZM15 12a3 3 0 1 1-6 0 3 3 0 0 1 6 0Z",
  cube: "m12 2 9 5v10l-9 5-9-5V7l9-5Zm0 10 9-5M3 7l9 5v10M7.5 4.5l9 5V15",
} as const;
export type IconName = keyof typeof paths;
export function Icon({
  name,
  size = 18,
  className = "",
  style,
}: {
  name: IconName;
  size?: number;
  className?: string;
  style?: CSSProperties;
}) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.7"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      className={className}
      style={style}
    >
      <path d={paths[name]} />
    </svg>
  );
}
