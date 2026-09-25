import { InventoryReservations } from '../../lib/parcel-kit.js';

function validationError(line, index, seen) {
  if (!line || typeof line.sku !== 'string' || !line.sku.trim()) {
    return { index, reason: 'SKU is required' };
  }
  if (!Number.isInteger(line.quantity) || line.quantity <= 0) {
    return { index, reason: 'Quantity must be a positive integer' };
  }
  if (seen.has(line.sku)) return { index, reason: 'Duplicate SKU' };
  seen.add(line.sku);
  return null;
}

function summarize(receipts) {
  return {
    reservedLines: receipts.length,
    reservedUnits: receipts.reduce((sum, receipt) => sum + receipt.quantity, 0),
    reservationIds: receipts.map(receipt => receipt.id),
  };
}

export async function reserveOrderLines(orderId, lines, observer) {
  if (!orderId || !Array.isArray(lines)) throw new TypeError('Invalid reservation request');
  const errors = [];
  const receipts = [];
  const seen = new Set();
  for (let index = 0; index < lines.length; index += 1) {
    const line = lines[index];
    const error = validationError(line, index, seen);
    if (error) {
      errors.push(error);
      continue;
    }
    const receipt = await InventoryReservations.reserve({
      orderId,
      sku: line.sku,
      quantity: line.quantity,
    });
    receipts.push(receipt);
    await observer.reserved({ index, reservationId: receipt.id });
  }
  const summary = summarize(receipts);
  return {
    accepted: errors.length === 0,
    errors,
    ...summary,
  };
}
