import { RecordDirectory } from '../../lib/parcel-kit.js';

function throwIfCancelled(signal) {
  if (signal?.aborted) {
    const error = new Error('Export cancelled');
    error.name = 'AbortError';
    throw error;
  }
}

function toExportRow(order) {
  return {
    id: order.id,
    customer: order.customerId,
    amountCents: order.totalCents,
    updatedAt: order.updatedAt,
  };
}

function summarize(rows) {
  const customers = new Set();
  let totalCents = 0;
  for (const row of rows) {
    customers.add(row.customer);
    totalCents += row.amountCents;
  }
  return { orderCount: rows.length, customerCount: customers.size, totalCents };
}

export async function exportOrders(filters, sink, signal) {
  throwIfCancelled(signal);
  const records = RecordDirectory.listAll(filters);
  const unique = new Map();
  for (const order of records) {
    throwIfCancelled(signal);
    const previous = unique.get(order.id);
    if (!previous || previous.updatedAt < order.updatedAt) {
      unique.set(order.id, order);
    }
  }
  const rows = [...unique.values()]
    .filter(order => order.status !== 'cancelled')
    .map(toExportRow)
    .sort((left, right) => left.id.localeCompare(right.id));
  const summary = summarize(rows);
  await sink.begin(summary);
  for (const row of rows) {
    throwIfCancelled(signal);
    await sink.write(row);
  }
  await sink.end(summary);
  return summary;
}
