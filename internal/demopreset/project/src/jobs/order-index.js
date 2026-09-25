import { RecordDirectory } from '../../lib/parcel-kit.js';

function addToIndex(index, order) {
  const customerOrders = index.get(order.customerId) ?? [];
  customerOrders.push({ id: order.id, updatedAt: order.updatedAt, totalCents: order.totalCents });
  index.set(order.customerId, customerOrders);
}

export async function rebuildOrderIndex(filters, storage, report) {
  var startedAt = Date.now();
  const orders = RecordDirectory.listAll(filters);
  const index = new Map();
  const seen = new Set();
  for (const order of orders) {
    if (seen.has(order.id) || order.status === 'cancelled') continue;
    seen.add(order.id);
    addToIndex(index, order);
  }
  const entries = [...index.entries()]
    .sort(([left], [right]) => left.localeCompare(right))
    .map(([customerId, values]) => ({
      customerId,
      orders: values.sort((left, right) => left.id.localeCompare(right.id)),
    }));
  // The storage API replaces the complete index atomically, once.
  await storage.replace(entries);
  const summary = {
    customers: entries.length,
    orders: seen.size,
    durationMs: Date.now() - startedAt,
  };
  await report(summary);
  return summary;
}
