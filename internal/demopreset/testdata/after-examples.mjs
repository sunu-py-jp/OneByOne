// Verify test-only reference implementations. Neither these checks nor the full
// solutions are shipped with the demo or used to evaluate AI output.
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { demoState, transactions, events, billing } from './lib/parcel-client.js';
import { placeOrder } from './src/orders/place-order.js';
import { createLiveOrders } from './src/ui/live-orders.js';
import { exportOrders } from './src/jobs/export-orders.js';
import { exportInvoices } from './src/billing/invoice-export.js';
import { processNextShipment } from './src/jobs/process-shipment.js';
import { submitPaymentRequest } from './src/billing/payment-request.js';
import { reconcilePayment } from './src/jobs/reconcile-payments.js';
import { reserveOrderLines } from './src/orders/bulk-reserve.js';
import { settleInvoice } from './src/billing/settle-invoice.js';
import { resolveRuntimeOptions } from './src/shared/runtime-options.js';

function shipment() {
  return { id: 'job-1', orderId: 'order-1', parcels: [{ code: 'a', weightGrams: 10 }], state: 'ready' };
}

function order() {
  return { id: 'order-test', customerId: 'customer-test', lines: [{ sku: 'sku-a', quantity: 2, unitPrice: 12 }] };
}

test('transaction closes on success/failure and notification failure cannot roll back persisted data', async () => {
  demoState.records.clear();
  const begin = transactions.begin;
  let closes = 0;
  transactions.begin = namespace => {
    const transaction = begin(namespace);
    const close = transaction.close;
    transaction.close = () => { closes += 1; close(); };
    return transaction;
  };
  try {
    await placeOrder(order(), async () => {});
    assert.equal(closes, 1);
    assert.equal(demoState.records.get('orders:order:order-test').totalCents, 2400);
    demoState.records.clear();
    demoState.failNextCommit = true;
    let notified = false;
    await assert.rejects(placeOrder(order(), async () => { notified = true; }), /Commit unavailable/);
    assert.equal(closes, 2);
    assert.equal(notified, false);
    assert.equal(demoState.records.size, 0);
    await assert.rejects(placeOrder(order(), async () => { throw new Error('Notification failed'); }), /Notification failed/);
    assert.equal(closes, 3);
    assert.equal(demoState.records.size, 2);
  } finally {
    transactions.begin = begin;
  }
});

test('a view closes only its own subscriptions, including partially failed startup', () => {
  let firstRenders = 0;
  let secondRenders = 0;
  const first = createLiveOrders({ render() { firstRenders += 1; }, setCount() {} }, 'shared');
  const second = createLiveOrders({ render() { secondRenders += 1; }, setCount() {} }, 'shared');
  first.start(); second.start();
  first.stop(); first.stop();
  events.emit('shared', 'order:changed', { id: 'a', updatedAt: 1 });
  assert.equal(firstRenders, 1);
  assert.equal(secondRenders, 2);
  second.stop();
  const subscribe = events.subscribe;
  let acquired = 0;
  let released = 0;
  events.subscribe = (...args) => {
    acquired += 1;
    if (acquired === 2) throw new Error('Second subscription unavailable');
    const subscription = subscribe(...args);
    return { close() { released += 1; subscription.close(); } };
  };
  try {
    const partial = createLiveOrders({ render() {}, setCount() {} }, 'partial');
    assert.throws(() => partial.start(), /Second subscription unavailable/);
    assert.equal(released, 1);
    assert.equal(partial.isActive(), false);
  } finally {
    events.subscribe = subscribe;
  }
});

test('pagination finishes collection before summary and ordered sink writes', async () => {
  const rows = [];
  let header;
  const summary = await exportOrders({}, {
    async begin(value) { header = value; assert.equal(value.orderCount, 4); },
    async write(value) { rows.push(value); },
    async end(value) { assert.deepEqual(value, header); },
  });
  assert.deepEqual(rows.map(row => row.id), ['order-1', 'order-2', 'order-4', 'order-5']);
  assert.equal(summary.totalCents, 7600);
  await assert.rejects(exportOrders({}, {}, { aborted: true }), { name: 'AbortError' });
});

test('reports are flushed before saved/complete notifications, and a flush error suppresses them', async () => {
  demoState.reports.clear();
  const invoices = [{ id: 'invoice-a', customerId: 'customer-a', items: [{ sku: 'a', quantity: 1, price: 2 }] }];
  const durableRows = () => [...demoState.reports.values()].reduce((sum, rows) => sum + rows.length, 0);
  await exportInvoices(invoices, {
    async saved(value) { assert.equal(durableRows(), value.rows); },
    async completed(value) { assert.equal(demoState.reports.get(value.reportId).length, 1); },
  });
  demoState.failNextFlush = true;
  let notification = false;
  await assert.rejects(exportInvoices(invoices, {
    async saved() { notification = true; },
    async completed() { notification = true; },
  }), /flush unavailable/);
  assert.equal(notification, false);
});

test('queue completes after persistence and releases a failed message for retry', async () => {
  demoState.queue.splice(0, demoState.queue.length, shipment());
  const carrier = { async createLabel() { return { trackingCode: 'track-a' }; } };
  await processNextShipment(carrier, {
    async save() { assert.equal(demoState.queue[0].state, 'leased'); },
  });
  assert.equal(demoState.queue[0].state, 'completed');
  demoState.queue.splice(0, demoState.queue.length, shipment());
  await assert.rejects(processNextShipment(carrier, {
    async save() { throw new Error('Storage unavailable'); },
  }), /Storage unavailable/);
  assert.equal(demoState.queue[0].state, 'ready');
});

test('sensitive debug payloads are removed while gateway input is preserved', async () => {
  demoState.audits.length = 0;
  demoState.rawDebug.length = 0;
  const request = {
    id: 'request-a', customerId: 'customer-a', debug: true,
    cardNumber: 'CARD-PLACEHOLDER', customerEmail: 'sample@example.invalid',
    paymentToken: 'DEMO-TOKEN', lines: [{ amountCents: 100 }], metadata: { private: 'private' },
  };
  await submitPaymentRequest(request, {
    async submit(value) {
      assert.equal(value.paymentToken, 'DEMO-TOKEN');
      return { id: 'receipt-a', internal: 'PRIVATE-RECEIPT' };
    },
  });
  assert.equal(demoState.rawDebug.length, 0);
  assert.equal(demoState.audits.length, 2);
  for (const entry of demoState.audits) {
    assert.deepEqual(Object.keys(entry).sort(), ['count', 'event', 'operationId', 'outcome']);
  }
  assert.equal(JSON.stringify(demoState.audits).includes('PRIVATE'), false);
});

test('retry preserves nested snapshot and one idempotency key despite observer mutation', async () => {
  demoState.payments.clear();
  demoState.failNextPaymentAfterSave = true;
  const payment = {
    id: 'pay-a', customerId: 'customer-a', amountCents: 500,
    lines: [{ sku: 'a', quantity: 1 }], metadata: { source: 'original' },
  };
  const result = await reconcilePayment(payment, {
    async attempting() {
      payment.id = 'changed-id';
      payment.lines[0].quantity = 99;
      payment.metadata.source = 'changed';
    },
    async succeeded() {},
    async failed() {},
  });
  assert.equal(result.attempts, 2);
  assert.equal(result.paymentId, 'pay-a');
  assert.equal(demoState.payments.size, 1);
  const saved = JSON.parse(demoState.payments.get('payment:pay-a').serialized);
  assert.equal(saved.lines[0].quantity, 1);
  assert.equal(saved.metadata.source, 'original');
  assert.equal('attempt' in saved, false);
});

test('all invalid-input checks precede any reservation, and validated values survive observer mutation', async () => {
  demoState.reservations.length = 0;
  let observed = 0;
  const invalid = await reserveOrderLines('order-a', [{ sku: 'a', quantity: 1 }, { sku: 'b', quantity: -1 }], {
    async reserved() { observed += 1; },
  });
  assert.equal(invalid.accepted, false);
  assert.equal(demoState.reservations.length, 0);
  assert.equal(observed, 0);
  const lines = [{ sku: 'a', quantity: 1 }, { sku: 'b', quantity: 2 }];
  await reserveOrderLines('order-a', lines, {
    async reserved() { lines[1].quantity = 99; },
  });
  assert.equal(demoState.reservations[1].quantity, 2);
});

test('only capture failure voids authorization and void failure cannot mask the capture error', async () => {
  demoState.authorizations.clear();
  await settleInvoice('invoice-success');
  assert.equal(demoState.authorizations.size, 1);
  demoState.authorizations.clear();
  demoState.failNextCapture = true;
  await assert.rejects(settleInvoice('invoice-failure'), /Capture declined/);
  assert.equal(demoState.authorizations.size, 0);
  const voidAuthorization = billing.void;
  billing.void = async () => { throw new Error('Void unavailable'); };
  demoState.failNextCapture = true;
  try {
    await assert.rejects(settleInvoice('invoice-double-failure'), /Capture declined/);
  } finally {
    billing.void = voidAuthorization;
  }
});

test('options distinguish missing/null/zero and preserve fractional seconds', () => {
  assert.equal(resolveRuntimeOptions({}).timeoutMs, 30000);
  assert.equal(resolveRuntimeOptions({ timeoutSeconds: null }).timeoutMs, null);
  assert.equal(resolveRuntimeOptions({ timeoutSeconds: 0 }).timeoutMs, 0);
  assert.equal(resolveRuntimeOptions({ timeoutSeconds: 1.25 }).timeoutMs, 1250);
  assert.equal(resolveRuntimeOptions({ retries: 0, label: '' }).attempts, 0);
});

// The large workflow is independently exercised before and after migration.
import { createDispatchSupervisor } from './src/workflows/dispatch-cycle.js';
import { createDispatchSupervisor as createInputDispatchSupervisor } from './src/workflows/dispatch-cycle.before.js';
import { directory, reports, payments, queue } from './lib/parcel-client.js';

function cycleRequest() {
  return {
    id: 'cycle-monday',
    invoiceId: 'invoice-monday',
    allocations: [
      { orderId: 'order-1', sku: ' box-a ', quantity: 2 },
      { orderId: 'order-4', sku: 'box-b', quantity: 3 },
    ],
    fee: {
      id: 'handling-monday', customerId: 'customer-a', amountCents: 600,
      lines: [{ code: 'handling', amountCents: 600 }],
      metadata: { department: { code: 'warehouse' } },
      paymentToken: 'DEMO-PRIVATE-TOKEN',
    },
    debug: true,
    customerEmail: 'demo@example.invalid',
  };
}

function resetDispatchState() {
  for (const name of ['records', 'reports', 'payments', 'authorizations']) demoState[name].clear();
  for (const name of ['audits', 'rawDebug', 'reservations']) demoState[name].length = 0;
  demoState.queue.splice(0, demoState.queue.length, shipment());
  for (const name of ['failNextCommit', 'failNextFlush', 'failNextCapture', 'failNextPaymentAfterSave']) {
    demoState[name] = false;
  }
}

function dispatchDependencies(overrides = {}) {
  return {
    observer: {
      view() {}, async planned() {}, async allocated() {}, async charging() {},
      async charged() {}, async chargeFailed() {}, async persisted() {},
      async reportSaved() {}, async reportCompleted() {}, ...overrides.observer,
    },
    carrier: { async createLabel() { return { trackingCode: 'dispatch-track' }; }, ...overrides.carrier },
    store: { async saveLabel() {}, ...overrides.store },
  };
}

test('large input workflow runs with the shipped SDK before migration', async () => {
  resetDispatchState();
  let previews = 0;
  const controller = createInputDispatchSupervisor(dispatchDependencies({
    observer: { async planned() { previews += 1; } },
  }));
  controller.start();
  try {
    const result = await controller.run(cycleRequest());
    assert.equal(result.status, 'completed');
    assert.equal(result.sourceCount, 5);
    assert.equal(result.orderCount, 4);
    assert.equal(result.reservedUnits, 5);
    assert.equal(result.rows, 4);
    assert.equal(previews, 1);
    assert.equal(demoState.queue[0].state, 'completed');
    assert.equal(demoState.records.get('dispatch:cycle:cycle-monday').cycleId, 'cycle-monday');
    assert.equal(demoState.reports.get(result.reportId).length, 4);
    assert.equal(controller.getSnapshot().completedCycles, 1);
  } finally {
    controller.stop();
  }
});

test('large migrated workflow combines all-page planning, immutable retry, durable boundaries and safe audit', async () => {
  resetDispatchState();
  const request = cycleRequest();
  demoState.failNextPaymentAfterSave = true;
  const fetchPage = directory.fetchPage;
  const begin = transactions.begin;
  const open = reports.open;
  const submit = payments.submit;
  const lease = queue.lease;
  let pages = 0;
  let transactionCloses = 0;
  let reportCloses = 0;
  let leaseCloses = 0;
  let paymentAttempts = 0;
  const observed = [];
  const notifiedFees = [];
  directory.fetchPage = async args => { pages += 1; return fetchPage(args); };
  transactions.begin = namespace => {
    const transaction = begin(namespace);
    return { ...transaction, close() { transactionCloses += 1; transaction.close(); } };
  };
  reports.open = name => {
    const writer = open(name);
    return { ...writer, close() { reportCloses += 1; writer.close(); } };
  };
  queue.lease = name => {
    const handle = lease(name);
    return handle && { ...handle, close() { leaseCloses += 1; handle.close(); } };
  };
  payments.submit = async (snapshot, options) => {
    paymentAttempts += 1;
    assert.equal(Object.isFrozen(snapshot), true);
    assert.equal(Object.isFrozen(snapshot.lines[0]), true);
    assert.equal(Object.isFrozen(snapshot.metadata.department), true);
    assert.equal(snapshot.paymentToken, 'DEMO-PRIVATE-TOKEN');
    assert.equal(snapshot.feeId, 'handling-monday');
    assert.equal(snapshot.lines[0].amountCents, 600);
    assert.equal(snapshot.metadata.department.code, 'warehouse');
    assert.equal('attempt' in snapshot || 'requestKey' in snapshot, false);
    return submit(snapshot, options);
  };
  const controller = createDispatchSupervisor(dispatchDependencies({
    observer: {
      async planned(summary) {
        assert.equal(pages, 3);
        assert.deepEqual(summary, { sourceCount: 5, orderCount: 4, customerCount: 3, totalCents: 7600 });
        observed.push('planned');
        // This callback precedes the entire charging phase, not only its retry.
        request.fee.id = 'edited-in-preview';
        request.fee.customerId = 'another-customer';
        request.fee.lines[0].amountCents = 999;
        request.fee.metadata.department.code = 'changed';
      },
      async allocated() { request.allocations[1].quantity = 40; },
      async charging(value) {
        notifiedFees.push(value.feeId);
        request.fee.id = 'edited-during-charge';
      },
      async charged(value) { notifiedFees.push(value.feeId); },
      async chargeFailed(value) { notifiedFees.push(value.feeId); },
      async persisted(value) {
        assert.equal(transactionCloses, 1);
        assert.equal(demoState.records.get('dispatch:cycle:' + value.cycleId).orderCount, 4);
        observed.push('persisted');
      },
      async reportSaved(value) {
        assert.equal(demoState.reports.get(value.reportId).length, value.rows);
        observed.push('saved:' + value.rows);
      },
      async reportCompleted(value) {
        assert.equal(demoState.reports.get(value.reportId).length, 4);
        observed.push('report-completed');
      },
    },
    store: {
      async saveLabel(value) {
        assert.equal(demoState.queue[0].state, 'leased');
        assert.equal(value.shipmentId, 'job-1');
        observed.push('label-saved');
      },
    },
  }), { label: '', retries: 1, timeoutSeconds: '1.5' });
  try {
    const result = await controller.run(request);
    assert.equal(result.status, 'completed');
    assert.equal(result.fee.feeId, 'handling-monday');
    assert.equal(result.fee.attempts, 2);
    assert.equal(result.reservedUnits, 5);
    assert.equal(paymentAttempts, 2);
    assert.equal(demoState.payments.size, 1);
    assert.deepEqual([...demoState.payments.keys()], ['dispatch-fee:cycle-monday:handling-monday']);
    assert.equal(notifiedFees.every(id => id === 'handling-monday'), true);
    assert.equal(demoState.reservations[1].quantity, 3);
    assert.equal(demoState.reservations[0].sku, 'box-a');
    assert.equal(transactionCloses, 1);
    assert.equal(reportCloses, 1);
    assert.equal(leaseCloses, 1);
    assert.equal(demoState.queue[0].state, 'completed');
    assert.deepEqual(observed, ['planned', 'label-saved', 'persisted', 'saved:2', 'saved:4', 'report-completed']);
    assert.deepEqual(demoState.reports.get(result.reportId).map(row => row.orderId), ['order-1', 'order-2', 'order-5', 'order-4']);
    assert.equal(controller.getSnapshot().running, false);
    assert.equal(controller.getSnapshot().options.label, '');
    assert.equal(controller.getSnapshot().options.timeoutMs, 1500);
    assert.equal(demoState.rawDebug.length, 0);
    assert.deepEqual(demoState.audits.map(entry => entry.event), ['dispatch-start', 'dispatch-complete']);
    for (const entry of demoState.audits) {
      assert.deepEqual(Object.keys(entry).sort(), ['count', 'event', 'operationId', 'outcome']);
    }
    assert.equal(JSON.stringify(demoState.audits).includes('PRIVATE'), false);
  } finally {
    directory.fetchPage = fetchPage;
    transactions.begin = begin;
    reports.open = open;
    payments.submit = submit;
    queue.lease = lease;
  }
});

test('large workflow rejects all allocation errors before any reservation or settlement', async () => {
  resetDispatchState();
  const request = cycleRequest();
  request.allocations.push(
    { orderId: 'order-1', sku: 'box-a', quantity: 1 },
    { orderId: 'order-4', sku: 'box-c', quantity: 0 },
    { orderId: 'order-missing', sku: 'box-d', quantity: 1 },
  );
  let allocated = 0;
  const controller = createDispatchSupervisor(dispatchDependencies({
    observer: { async allocated() { allocated += 1; } },
  }));
  const result = await controller.run(request);
  assert.equal(result.status, 'invalid');
  assert.deepEqual(result.errors, [
    { index: 2, reason: 'Duplicate order allocation' },
    { index: 3, reason: 'Quantity must be between one and fifty' },
    { index: 4, reason: 'Order is not in the dispatch plan' },
  ]);
  assert.equal(result.reservedUnits, 0);
  assert.equal(demoState.reservations.length, 0);
  assert.equal(allocated, 0);
  assert.equal(demoState.authorizations.size, 0);
  assert.equal(demoState.payments.size, 0);
  assert.equal(demoState.queue[0].state, 'ready');
  assert.equal(demoState.audits.at(-1).outcome, 'invalid');
});

test('large workflow subscriptions survive another owner stopping and release partial startup', () => {
  const first = createDispatchSupervisor(dispatchDependencies(), { scope: 'dispatch-shared' });
  const second = createDispatchSupervisor(dispatchDependencies(), { scope: 'dispatch-shared' });
  first.start(); second.start(); first.stop();
  events.emit('dispatch-shared', 'order:changed', { id: 'order-update', updatedAt: 77 });
  assert.equal(first.getSnapshot().lastUpdate, null);
  assert.deepEqual(second.getSnapshot().lastUpdate, { orderId: 'order-update', updatedAt: 77 });
  first.start(); first.start();
  events.emit('dispatch-shared', 'order:changed', { id: 'order-next', updatedAt: 78 });
  assert.equal(first.getSnapshot().lastUpdate.updatedAt, 78);
  first.stop(); second.stop();
  const subscribe = events.subscribe;
  let acquired = 0;
  let released = 0;
  events.subscribe = (...args) => {
    acquired += 1;
    if (acquired === 2) throw new Error('Subscription limit reached');
    const owned = subscribe(...args);
    return { close() { released += 1; owned.close(); } };
  };
  try {
    const partial = createDispatchSupervisor(dispatchDependencies(), { scope: 'dispatch-partial' });
    assert.throws(() => partial.start(), /Subscription limit reached/);
    assert.equal(released, 1);
    assert.equal(partial.getSnapshot().active, false);
  } finally {
    events.subscribe = subscribe;
  }
});

test('large workflow observes cancellation after page await before publishing the plan', async () => {
  resetDispatchState();
  const fetchPage = directory.fetchPage;
  const signal = { aborted: false };
  let planned = false;
  directory.fetchPage = async args => {
    const page = await fetchPage(args);
    signal.aborted = true;
    return page;
  };
  try {
    const controller = createDispatchSupervisor(dispatchDependencies({
      observer: { async planned() { planned = true; } },
    }));
    await assert.rejects(controller.run(cycleRequest(), signal), { name: 'AbortError' });
    assert.equal(planned, false);
    assert.equal(demoState.reservations.length, 0);
    assert.equal(controller.getSnapshot().running, false);
  } finally {
    directory.fetchPage = fetchPage;
  }
});

test('large workflow label storage failure releases the lease for retry', async () => {
  resetDispatchState();
  const lease = queue.lease;
  let closes = 0;
  queue.lease = name => {
    const owned = lease(name);
    return owned && { ...owned, close() { closes += 1; owned.close(); } };
  };
  try {
    const controller = createDispatchSupervisor(dispatchDependencies({
      store: { async saveLabel() { throw new Error('Label store unavailable'); } },
    }));
    await assert.rejects(controller.run(cycleRequest()), /Label store unavailable/);
    assert.equal(demoState.queue[0].state, 'ready');
    assert.equal(demoState.queue[0].lastError, 'Label store unavailable');
    assert.equal(closes, 1);
    assert.equal(demoState.records.size, 0);
    assert.equal(demoState.reports.size, 0);
    assert.equal(demoState.audits.at(-1).outcome, 'failed');
  } finally {
    queue.lease = lease;
  }
});

test('large workflow commit and notification failures have different rollback boundaries', async () => {
  const begin = transactions.begin;
  let closes = 0;
  let rollbacks = 0;
  transactions.begin = namespace => {
    const transaction = begin(namespace);
    return {
      ...transaction,
      async rollback() { rollbacks += 1; await transaction.rollback(); },
      close() { closes += 1; transaction.close(); },
    };
  };
  try {
    resetDispatchState();
    demoState.failNextCommit = true;
    let persisted = false;
    const failed = createDispatchSupervisor(dispatchDependencies({
      observer: { async persisted() { persisted = true; } },
    }));
    await assert.rejects(failed.run(cycleRequest()), /Commit unavailable/);
    assert.equal(demoState.records.size, 0);
    assert.equal(persisted, false);
    assert.equal(closes, 1);
    assert.equal(rollbacks, 1);
    resetDispatchState();
    const notified = createDispatchSupervisor(dispatchDependencies({
      observer: { async persisted() { throw new Error('Observer unavailable'); } },
    }));
    await assert.rejects(notified.run(cycleRequest()), /Observer unavailable/);
    assert.equal(demoState.records.size, 4);
    assert.equal(closes, 2);
    assert.equal(rollbacks, 1);
    assert.equal(demoState.authorizations.size, 1);
  } finally {
    transactions.begin = begin;
  }
});

test('large workflow flush failure prevents report notifications and closes the writer', async () => {
  resetDispatchState();
  demoState.failNextFlush = true;
  const open = reports.open;
  let closes = 0;
  let notified = false;
  reports.open = name => {
    const writer = open(name);
    return { ...writer, close() { closes += 1; writer.close(); } };
  };
  try {
    const controller = createDispatchSupervisor(dispatchDependencies({
      observer: {
        async reportSaved() { notified = true; },
        async reportCompleted() { notified = true; },
      },
    }));
    await assert.rejects(controller.run(cycleRequest()), /Report flush unavailable/);
    assert.equal(notified, false);
    assert.equal(closes, 1);
    assert.equal([...demoState.reports.values()][0].length, 0);
    assert.equal(demoState.records.size, 4);
  } finally {
    reports.open = open;
  }
});

test('large workflow compensates only failed captures and preserves their original errors', async () => {
  resetDispatchState();
  demoState.failNextCapture = true;
  const controller = createDispatchSupervisor(dispatchDependencies());
  await assert.rejects(controller.run(cycleRequest()), /Capture declined/);
  assert.equal(demoState.authorizations.size, 0);
  const voidAuthorization = billing.void;
  billing.void = async () => { throw new Error('Compensation unavailable'); };
  demoState.failNextCapture = true;
  try {
    await assert.rejects(controller.run(cycleRequest()), /Capture declined/);
    assert.equal(demoState.payments.size, 0);
  } finally {
    billing.void = voidAuthorization;
  }
});

test('large workflow options preserve omitted, null, zero and explicit empty values', () => {
  for (const [settings, expected] of [
    [{}, { timeoutMs: 30000, attempts: 2, label: 'Dispatch desk', scope: 'dispatch' }],
    [{ timeoutSeconds: null, retries: 0, label: '', scope: '' }, { timeoutMs: null, attempts: 0, label: '', scope: '' }],
    [{ timeoutSeconds: 0 }, { timeoutMs: 0, attempts: 2, label: 'Dispatch desk', scope: 'dispatch' }],
  ]) {
    const { reportEvery, ...actual } = createDispatchSupervisor(dispatchDependencies(), settings).getSnapshot().options;
    assert.deepEqual(actual, expected);
    assert.equal(reportEvery, 2);
  }
  assert.throws(() => createDispatchSupervisor(dispatchDependencies(), { retries: 1.5 }), /Retries/);
});
