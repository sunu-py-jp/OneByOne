const LABELS = Object.freeze({
  draft: 'Draft',
  submitted: 'Submitted',
  paid: 'Paid',
  shipped: 'Shipped',
  cancelled: 'Cancelled',
});

const ORDER = ['draft', 'submitted', 'paid', 'shipped', 'cancelled'];

export function statusLabel(status) {
  return LABELS[status] ?? 'Unknown';
}

export function statusChoices(allowed) {
  return ORDER.filter(status => allowed.includes(status))
    .map(status => ({ value: status, label: LABELS[status] }));
}

export function summarizeStatuses(orders) {
  const counts = new Map();
  for (const order of orders) {
    counts.set(order.status, (counts.get(order.status) ?? 0) + 1);
  }
  return ORDER.map(status => ({ status, label: LABELS[status], count: counts.get(status) ?? 0 }));
}
