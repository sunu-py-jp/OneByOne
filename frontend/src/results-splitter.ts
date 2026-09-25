export const RESULTS_SPLITTER_WIDTH = 9;

export function resultSplitBounds(containerWidth: number) {
  const available = Math.max(0, containerWidth - RESULTS_SPLITTER_WIDTH);
  const min = Math.min(240, Math.floor(available * 0.42));
  const max = Math.max(min, available - Math.min(320, Math.ceil(available * 0.58)));
  return { min, max, available };
}

export function clampResultListWidth(width: number, containerWidth: number) {
  const { min, max } = resultSplitBounds(containerWidth);
  return Math.round(Math.min(max, Math.max(min, width)));
}

export function fitResultListWidth(contentWidth: number, containerWidth: number) {
  const { available } = resultSplitBounds(containerWidth);
  // Long paths must not take the space needed to read the change itself.
  return clampResultListWidth(Math.min(contentWidth, 460, available * 0.4), containerWidth);
}
