// Parcel Kit SDK: in-memory implementation for this offline project.
import { transactions, events, selectDirectoryEntries, reservations, billing, runtimeOptions, demoState } from './parcel-client.js';
let sequence = 1;
const scopeSubscriptions = new Map();
const heldMessages = new Map();

export const TransactionManager = {
  open(namespace) {
    const transaction = transactions.begin(namespace);
    return {
      save: (key, value) => transaction.put(key, value),
      async commit() { await transaction.commit(); transaction.close(); },
      async rollback() { await transaction.rollback(); transaction.close(); },
    };
  },
};

export const EventBus = {
  on(scope, topic, handler) {
    const subscription = events.subscribe(scope, topic, handler);
    const owned = scopeSubscriptions.get(scope) ?? [];
    owned.push(subscription);
    scopeSubscriptions.set(scope, owned);
  },
  clear(scope) {
    for (const subscription of scopeSubscriptions.get(scope) ?? []) subscription.close();
    scopeSubscriptions.delete(scope);
  },
};

export const RecordDirectory = {
  listAll: filters => selectDirectoryEntries(filters),
  count: filters => selectDirectoryEntries(filters).length,
};

export const ReportWriter = {
  open(name) {
    const id = 'v1-' + name + '-' + sequence++;
    let closed = false;
    demoState.reports.set(id, []);
    return {
      id,
      add(row) {
        if (closed) throw new Error('Report is closed');
        demoState.reports.get(id).push(structuredClone(row));
      },
      finish() { closed = true; },
    };
  },
};

export const WorkQueue = {
  take(name) {
    if (name !== 'shipments') return null;
    const record = demoState.queue.find(item => item.state === 'ready');
    if (!record) return null;
    record.state = 'leased';
    heldMessages.set(record.id, record);
    return structuredClone(record);
  },
  ack(id) {
    const record = heldMessages.get(id);
    if (record) record.state = 'completed';
    heldMessages.delete(id);
  },
  fail(id, reason) {
    const record = heldMessages.get(id);
    if (record) { record.state = 'ready'; record.lastError = reason; }
    heldMessages.delete(id);
  },
};

export const RequestTelemetry = {
  capturePayload(event, payload) { demoState.rawDebug.push({ event, payload: structuredClone(payload) }); },
};

export const PaymentGateway = {
  async send(request) {
    const receipt = { id: 'v1-payment-' + sequence++, amountCents: request.amountCents };
    demoState.payments.set(request.requestKey, { serialized: JSON.stringify(request), receipt });
    if (demoState.failNextPaymentAfterSave) {
      demoState.failNextPaymentAfterSave = false;
      const error = new Error('Reply was interrupted after saving');
      error.retryable = true;
      throw error;
    }
    return structuredClone(receipt);
  },
};

export const InventoryReservations = { reserve: request => reservations.reserve(request) };

function callbackResult(promise, callback) {
  promise.then(value => callback(null, value), error => callback(error));
}
export const InvoiceService = {
  lookup(id, callback) { callbackResult(billing.lookup(id), callback); },
  authorize(invoice, callback) { callbackResult(billing.authorize(invoice), callback); },
  capture(authorization, callback) { callbackResult(billing.capture(authorization), callback); },
  void(id, callback) { callbackResult(billing.void(id), callback); },
};

export const ClientOptions = {
  resolve({ timeoutSeconds, retries, label }) {
    return runtimeOptions.resolve({
      request: { timeoutMs: timeoutSeconds === undefined ? undefined : timeoutSeconds === null ? null : timeoutSeconds * 1000 },
      retry: { attempts: retries },
      display: { label },
    });
  },
};
