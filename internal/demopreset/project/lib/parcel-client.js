// Parcel Client SDK: memory-only APIs, no network or installed packages.
const clone = value => structuredClone(value);
let sequence = 1;
const nextId = prefix => prefix + '-' + sequence++;
const entries = [
  { id: 'order-1', customerId: 'customer-a', totalCents: 2400, updatedAt: 10, status: 'paid' },
  { id: 'order-2', customerId: 'customer-b', totalCents: 900, updatedAt: 12, status: 'submitted' },
  { id: 'order-3', customerId: 'customer-a', totalCents: 1700, updatedAt: 15, status: 'cancelled' },
  { id: 'order-4', customerId: 'customer-c', totalCents: 3100, updatedAt: 16, status: 'shipped' },
  { id: 'order-5', customerId: 'customer-b', totalCents: 1200, updatedAt: 19, status: 'paid' },
];

// Exposed solely for optional local verification and dependency injection.
export const demoState = {
  records: new Map(),
  reports: new Map(),
  audits: [],
  rawDebug: [],
  reservations: [],
  payments: new Map(),
  authorizations: new Map(),
  queue: [{ id: 'shipment-1', orderId: 'order-1', parcels: [{ code: 'box-a', weightGrams: 800 }], state: 'ready' }],
  failNextCommit: false,
  failNextFlush: false,
  failNextCapture: false,
  failNextPaymentAfterSave: false,
};

export const transactions = {
  begin(namespace) {
    let state = 'open';
    const pending = new Map();
    return {
      put(key, value) {
        if (state !== 'open') throw new Error('Transaction is not open');
        pending.set(namespace + ':' + key, clone(value));
      },
      async commit() {
        if (state !== 'open') throw new Error('Transaction is not open');
        if (demoState.failNextCommit) {
          demoState.failNextCommit = false;
          throw new Error('Commit unavailable');
        }
        for (const [key, value] of pending) demoState.records.set(key, value);
        state = 'committed';
      },
      async rollback() {
        if (state === 'open') { pending.clear(); state = 'rolled-back'; }
      },
      close() { pending.clear(); state = 'closed'; },
    };
  },
};

const subscriptions = new Set();
export const events = {
  subscribe(scope, topic, handler) {
    if (typeof handler !== 'function') throw new TypeError('Handler is required');
    const item = { scope, topic, handler };
    subscriptions.add(item);
    return { close() { subscriptions.delete(item); } };
  },
  emit(scope, topic, value) {
    for (const item of [...subscriptions]) {
      if (item.scope === scope && item.topic === topic) item.handler(clone(value));
    }
  },
};

export function selectDirectoryEntries(filters = {}) {
  return clone(entries.filter(order => !filters.customerId || order.customerId === filters.customerId));
}
export const directory = {
  async fetchPage({ filters = {}, cursor = null, limit = 100 } = {}) {
    if (!Number.isInteger(limit) || limit < 1) throw new TypeError('Invalid page limit');
    const offset = cursor === null ? 0 : Number(cursor);
    if (!Number.isInteger(offset) || offset < 0) throw new TypeError('Invalid cursor');
    const records = selectDirectoryEntries(filters);
    // The adapter caps pages at two rows, so a real pagination loop is necessary.
    const pageSize = Math.min(limit, 2);
    const items = records.slice(offset, offset + pageSize);
    const next = offset + pageSize;
    return { items, nextCursor: next < records.length ? String(next) : null };
  },
};

export const reports = {
  open(name) {
    const id = nextId(name);
    const pending = [];
    let closed = false;
    demoState.reports.set(id, []);
    return {
      id,
      write(row) {
        if (closed) throw new Error('Report is closed');
        pending.push(clone(row));
      },
      async flush() {
        if (closed) throw new Error('Report is closed');
        if (demoState.failNextFlush) {
          demoState.failNextFlush = false;
          throw new Error('Report flush unavailable');
        }
        demoState.reports.get(id).push(...pending.splice(0));
      },
      close() { pending.length = 0; closed = true; },
    };
  },
};

export const queue = {
  lease(name) {
    if (name !== 'shipments') return null;
    const record = demoState.queue.find(item => item.state === 'ready');
    if (!record) return null;
    record.state = 'leased';
    let resolved = false;
    return {
      message: clone(record),
      complete() {
        if (resolved) return;
        record.state = 'completed';
        resolved = true;
      },
      retry(reason) {
        if (resolved) return;
        record.state = 'ready';
        record.lastError = reason;
        resolved = true;
      },
      close() {
        if (!resolved) record.state = 'ready';
        resolved = true;
      },
    };
  },
};

export const audits = {
  record(event, { operationId, count, outcome }) {
    demoState.audits.push({ event, operationId, count, outcome });
  },
};

export const payments = {
  async submit(snapshot, { idempotencyKey }) {
    if (!Object.isFrozen(snapshot) || 'attempt' in snapshot || 'requestKey' in snapshot) {
      throw new TypeError('Expected an immutable business snapshot');
    }
    if (!idempotencyKey) throw new TypeError('Idempotency key is required');
    const serialized = JSON.stringify(snapshot);
    const previous = demoState.payments.get(idempotencyKey);
    if (previous && previous.serialized !== serialized) throw new Error('Idempotency payload mismatch');
    const receipt = previous?.receipt ?? { id: nextId('payment'), amountCents: snapshot.amountCents };
    demoState.payments.set(idempotencyKey, { serialized, receipt });
    if (demoState.failNextPaymentAfterSave) {
      demoState.failNextPaymentAfterSave = false;
      const error = new Error('Reply was interrupted after saving');
      error.retryable = true;
      throw error;
    }
    return clone(receipt);
  },
};

export const reservations = {
  async reserve({ orderId, sku, quantity }) {
    if (!orderId || !sku || !Number.isInteger(quantity) || quantity <= 0) {
      throw new TypeError('Invalid reservation');
    }
    const receipt = { id: nextId('reservation'), orderId, sku, quantity };
    demoState.reservations.push(receipt);
    return clone(receipt);
  },
};

export const billing = {
  async lookup(id) {
    return { id, customerId: 'customer-a', totalCents: 2400, status: 'submitted' };
  },
  async authorize(invoice) {
    const authorization = { id: nextId('authorization'), invoiceId: invoice.id, amountCents: invoice.totalCents };
    demoState.authorizations.set(authorization.id, authorization);
    return clone(authorization);
  },
  async capture(authorization) {
    if (!demoState.authorizations.has(authorization.id)) throw new Error('Unknown authorization');
    if (demoState.failNextCapture) {
      demoState.failNextCapture = false;
      throw new Error('Capture declined');
    }
    return { id: nextId('receipt'), amountCents: authorization.amountCents };
  },
  async void(id) { demoState.authorizations.delete(id); },
};

export const runtimeOptions = {
  resolve({ request = {}, retry = {}, display = {} }) {
    return {
      timeoutMs: request.timeoutMs === undefined ? 30000 : request.timeoutMs,
      attempts: retry.attempts === undefined ? 2 : retry.attempts,
      label: display.label ?? '',
    };
  },
};
