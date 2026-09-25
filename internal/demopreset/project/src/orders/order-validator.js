export function validateOrderDraft(draft) {
  const errors = [];
  if (!draft || typeof draft !== 'object') {
    return [{ field: 'order', message: 'Order is required' }];
  }
  if (typeof draft.customerId !== 'string' || !draft.customerId.trim()) {
    errors.push({ field: 'customerId', message: 'Customer is required' });
  }
  if (!Array.isArray(draft.lines) || draft.lines.length === 0) {
    errors.push({ field: 'lines', message: 'At least one line is required' });
    return errors;
  }
  const seen = new Set();
  for (let index = 0; index < draft.lines.length; index += 1) {
    const line = draft.lines[index];
    if (!line || typeof line.sku !== 'string' || !line.sku.trim()) {
      errors.push({ field: 'lines.' + index + '.sku', message: 'SKU is required' });
      continue;
    }
    if (seen.has(line.sku)) {
      errors.push({ field: 'lines.' + index + '.sku', message: 'SKU must be unique' });
    }
    seen.add(line.sku);
    if (!Number.isInteger(line.quantity) || line.quantity <= 0) {
      errors.push({ field: 'lines.' + index + '.quantity', message: 'Quantity must be positive' });
    }
  }
  return errors;
}
