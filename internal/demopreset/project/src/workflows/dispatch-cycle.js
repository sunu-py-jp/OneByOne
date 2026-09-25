import {
  TransactionManager,
  EventBus,
  RecordDirectory,
  ReportWriter,
  WorkQueue,
  RequestTelemetry,
  PaymentGateway,
  InventoryReservations,
  InvoiceService,
  ClientOptions,
} from '../../lib/parcel-kit.js';

// A dispatch cycle owns its subscriptions, SDK handles and completion promises.
// Observer methods may change caller-owned data and may reject. Store methods
// resolve only after durable storage; carrier requests use the shipment ID for
// deduplication. A cycle ID identifies one immutable business request.
// The handling fee uses the values submitted when run starts, even if a progress
// callback edits the caller's payment form before the charging phase begins.
function numericOption(value, name) {
  if (value === null || value === undefined) return value;
  const number = Number(value);
  if (!Number.isFinite(number) || number < 0) {
    throw new TypeError(name + ' must be a non-negative number');
  }
  return number;
}

function configure(settings) {
  const timeout = numericOption(settings.timeoutSeconds, 'Timeout');
  const retries = settings.retries === undefined ? 2 : Number(settings.retries);
  if (!Number.isInteger(retries) || retries < 0 || retries > 4) {
    throw new TypeError('Retries must be between zero and four');
  }
  const label = settings.label == null ? 'Dispatch desk' : settings.label;
  const effective = ClientOptions.resolve({ timeoutSeconds: timeout, retries, label });
  return {
    ...effective,
    scope: settings.scope == null ? 'dispatch' : settings.scope,
    reportEvery: 2,
  };
}

function assertActive(signal) {
  if (!signal?.aborted) return;
  const error = new Error('Dispatch cycle cancelled');
  error.name = 'AbortError';
  throw error;
}

function validateCycle(request) {
  if (!request || typeof request.id !== 'string' || !/^[a-z0-9-]+$/i.test(request.id)) {
    throw new TypeError('Cycle ID must be an opaque alphanumeric identifier');
  }
  if (!Array.isArray(request.allocations)) {
    throw new TypeError('Allocations must be an array');
  }
  if (!request.invoiceId || !request.fee || !request.fee.id || !request.fee.customerId) {
    throw new TypeError('Invoice and handling fee identities are required');
  }
  if (!Number.isInteger(request.fee.amountCents) || request.fee.amountCents <= 0) {
    throw new TypeError('Handling fee must be positive');
  }
  if (!Array.isArray(request.fee.lines)) {
    throw new TypeError('Handling fee lines are required');
  }
}

function buildPlan(records, sourceCount, requestedOrders) {
  const newest = new Map();
  const requested = new Set(requestedOrders);
  var totalCents = 0;
  for (const record of records) {
    if (record.status === 'cancelled') continue;
    if (requested.size && !requested.has(record.id)) continue;
    const previous = newest.get(record.id);
    if (!previous || previous.updatedAt < record.updatedAt) {
      newest.set(record.id, record);
    }
  }
  const orders = [...newest.values()].sort((left, right) => {
    return left.customerId.localeCompare(right.customerId) || left.id.localeCompare(right.id);
  });
  const customers = new Map();
  for (const order of orders) {
    totalCents += order.totalCents;
    const group = customers.get(order.customerId) ?? [];
    group.push(order.id);
    customers.set(order.customerId, group);
  }
  return {
    sourceCount,
    orders,
    totalCents,
    customerGroups: [...customers].map(([customerId, orderIds]) => ({ customerId, orderIds })),
  };
}

async function collectPlan(request, observer, signal) {
  assertActive(signal);
  const filters = request.customerId ? { customerId: request.customerId } : {};
  const records = RecordDirectory.listAll(filters);
  const sourceCount = RecordDirectory.count(filters);
  const requestedOrders = request.orderIds == null ? [] : request.orderIds;
  if (!Array.isArray(requestedOrders)) throw new TypeError('Order IDs must be an array');
  const plan = buildPlan(records, sourceCount, requestedOrders);
  // Consumers replace the complete preview, so this notification occurs once.
  await observer.planned({
    sourceCount: plan.sourceCount,
    orderCount: plan.orders.length,
    customerCount: plan.customerGroups.length,
    totalCents: plan.totalCents,
  });
  return plan;
}

function allocationError(line, index, availableOrders, seen) {
  if (!line || !availableOrders.has(line.orderId)) {
    return { index, reason: 'Order is not in the dispatch plan' };
  }
  if (typeof line.sku !== 'string' || !line.sku.trim()) {
    return { index, reason: 'SKU is required' };
  }
  if (!Number.isInteger(line.quantity) || line.quantity < 1 || line.quantity > 50) {
    return { index, reason: 'Quantity must be between one and fifty' };
  }
  const key = line.orderId + ':' + line.sku.trim();
  if (seen.has(key)) return { index, reason: 'Duplicate order allocation' };
  seen.add(key);
  return null;
}

async function allocateInventory(allocations, plan, observer, signal) {
  const availableOrders = new Set(plan.orders.map(order => order.id));
  const seen = new Set();
  const errors = [];
  const receipts = [];
  for (let index = 0; index < allocations.length; index += 1) {
    assertActive(signal);
    const line = allocations[index];
    const error = allocationError(line, index, availableOrders, seen);
    if (error) {
      errors.push(error);
      continue;
    }
    const receipt = await InventoryReservations.reserve({
      orderId: line.orderId,
      sku: line.sku.trim(),
      quantity: line.quantity,
    });
    receipts.push(receipt);
    await observer.allocated({ index, reservationId: receipt.id });
  }
  return {
    accepted: errors.length === 0,
    errors,
    receipts,
    reservedUnits: receipts.reduce((sum, receipt) => sum + receipt.quantity, 0),
  };
}

function validateSettlement(invoice) {
  if (!invoice || !invoice.id || invoice.status === 'cancelled') {
    throw new Error('Invoice is unavailable for settlement');
  }
  if (!Number.isInteger(invoice.totalCents) || invoice.totalCents <= 0) {
    throw new TypeError('Invoice total is invalid');
  }
}

function settleAccount(invoiceId, expectedCustomer) {
  return new Promise((resolve, reject) => {
    InvoiceService.lookup(invoiceId, (lookupError, invoice) => {
      if (lookupError) {
        reject(lookupError);
        return;
      }
      try {
        validateSettlement(invoice);
        if (invoice.customerId !== expectedCustomer) {
          throw new Error('Invoice belongs to another account');
        }
      } catch (error) {
        reject(error);
        return;
      }
      InvoiceService.authorize(invoice, (authorizationError, authorization) => {
        if (authorizationError) {
          reject(authorizationError);
          return;
        }
        InvoiceService.capture(authorization, (captureError, receipt) => {
          if (captureError) {
            InvoiceService.void(authorization.id, () => reject(captureError));
            return;
          }
          resolve({
            invoiceId: invoice.id,
            receiptId: receipt.id,
            totalCents: invoice.totalCents,
          });
        });
      });
    });
  });
}

async function collectHandlingFee(cycleId, fee, observer, attempts, signal) {
  // Fee data is a JSON-compatible business object; callbacks may edit the form.
  const request = {
    cycleId,
    feeId: fee.id,
    customerId: fee.customerId,
    amountCents: fee.amountCents,
    lines: fee.lines,
    metadata: fee.metadata,
    paymentToken: fee.paymentToken,
  };
  for (let attempt = 1; attempt <= attempts; attempt += 1) {
    assertActive(signal);
    request.attempt = attempt;
    request.requestKey = cycleId + ':' + fee.id + ':' + attempt;
    await observer.charging({ feeId: fee.id, attempt });
    try {
      const receipt = await PaymentGateway.send(request);
      await observer.charged({ feeId: fee.id, receiptId: receipt.id, attempt });
      return { feeId: fee.id, receiptId: receipt.id, attempts: attempt };
    } catch (error) {
      const retryable = error?.retryable === true;
      await observer.chargeFailed({ feeId: fee.id, attempt, retryable });
      if (!retryable || attempt === attempts) throw error;
    }
  }
}

function validateShipment(message) {
  if (!message.orderId || !Array.isArray(message.parcels) || !message.parcels.length) {
    throw new TypeError('Shipment parcels are required');
  }
  for (const parcel of message.parcels) {
    if (!parcel.code || !Number.isInteger(parcel.weightGrams) || parcel.weightGrams <= 0) {
      throw new TypeError('Parcel weight must be a positive integer');
    }
  }
}

async function dispatchShipment(carrier, store, signal) {
  assertActive(signal);
  const message = WorkQueue.take('shipments');
  if (!message) return null;
  // This job's durable result is a stored label, not the later cycle report.
  WorkQueue.ack(message.id);
  try {
    validateShipment(message);
    const label = await carrier.createLabel({
      orderId: message.orderId,
      parcels: message.parcels,
      idempotencyKey: 'shipment:' + message.id,
    });
    assertActive(signal);
    const record = {
      shipmentId: message.id,
      orderId: message.orderId,
      trackingCode: label.trackingCode,
      parcelCount: message.parcels.length,
    };
    await store.saveLabel(record);
    return record;
  } catch (error) {
    WorkQueue.fail(message.id, error.message);
    throw error;
  }
}

async function storeCycle(cycleId, plan, result, observer) {
  const transaction = TransactionManager.open('dispatch');
  try {
    transaction.save('cycle:' + cycleId, result);
    for (const group of plan.customerGroups) {
      transaction.save('account:' + group.customerId + ':' + cycleId, {
        cycleId,
        orderIds: group.orderIds,
        receiptId: result.settlement.receiptId,
      });
    }
    await transaction.commit();
  } catch (error) {
    await transaction.rollback();
    throw error;
  }
  // A notification failure must not undo the committed cycle.
  await observer.persisted({ cycleId, orderCount: plan.orders.length });
}

function reportRow(order, result) {
  return {
    orderId: order.id,
    customerId: order.customerId,
    totalCents: order.totalCents,
    cycleId: result.cycleId,
    invoiceReceipt: result.settlement.receiptId,
    feeReceipt: result.fee.receiptId,
    trackingCode: result.shipment?.orderId === order.id ? result.shipment.trackingCode : '',
  };
}

async function publishReport(plan, result, observer, reportEvery, signal) {
  const writer = ReportWriter.open('dispatch-' + result.cycleId);
  var saved = 0;
  try {
    for (const order of plan.orders) {
      assertActive(signal);
      writer.add(reportRow(order, result));
      saved += 1;
      if (saved % reportEvery === 0) {
        // The observer reads these saved rows from the report store immediately.
        await observer.reportSaved({ reportId: writer.id, rows: saved });
      }
    }
    await observer.reportCompleted({
      reportId: writer.id,
      rows: saved,
      totalCents: plan.totalCents,
    });
    return { reportId: writer.id, rows: saved };
  } finally {
    writer.finish();
  }
}

function rejection(cycleId, allocation) {
  return {
    cycleId,
    status: 'invalid',
    errors: allocation.errors,
    reservedUnits: allocation.reservedUnits,
    orderCount: 0,
  };
}

export function createDispatchSupervisor({ observer, carrier, store }, settings = {}) {
  const options = configure(settings);
  var active = false;
  var running = false;
  var generation = 0;
  var completedCycles = 0;
  var lastCycle = null;
  var lastUpdate = null;

  function notifyView() {
    observer.view({
      active,
      running,
      completedCycles,
      lastCycle,
      lastUpdate,
      label: options.label,
    });
  }

  function stop() {
    if (!active) return;
    active = false;
    generation += 1;
    EventBus.clear(options.scope);
  }

  function start() {
    if (active) return;
    active = true;
    generation += 1;
    const started = generation;
    try {
      EventBus.on(options.scope, 'order:changed', value => {
        if (!active || generation !== started) return;
        lastUpdate = { orderId: value.id, updatedAt: value.updatedAt };
        notifyView();
      });
      EventBus.on(options.scope, 'dispatch:refresh', () => {
        if (!active || generation !== started) return;
        notifyView();
      });
      notifyView();
    } catch (error) {
      stop();
      throw error;
    }
  }

  async function run(request, signal) {
    if (running) throw new Error('A dispatch cycle is already running');
    validateCycle(request);
    assertActive(signal);
    const cycleId = request.id;
    const allocationCount = request.allocations.length;
    // Support metadata is only diagnostic; it is not used for business decisions.
    const diagnostic = JSON.stringify(request);
    RequestTelemetry.capturePayload('dispatch-start', diagnostic);
    if (request.debug) {
      RequestTelemetry.capturePayload('dispatch-support', {
        email: request.customerEmail,
        token: request.fee.paymentToken,
        metadata: request.fee.metadata,
      });
    }
    running = true;
    try {
      const plan = await collectPlan(request, observer, signal);
      const allocation = await allocateInventory(request.allocations, plan, observer, signal);
      if (!allocation.accepted) {
        const invalid = rejection(cycleId, allocation);
        RequestTelemetry.capturePayload('dispatch-invalid', { request, errors: allocation.errors });
        return invalid;
      }
      assertActive(signal);
      const settlement = await settleAccount(request.invoiceId, request.fee.customerId);
      const fee = await collectHandlingFee(cycleId, request.fee, observer, options.attempts + 1, signal);
      const shipment = await dispatchShipment(carrier, store, signal);
      const result = {
        cycleId,
        status: 'completed',
        orderCount: plan.orders.length,
        sourceCount: plan.sourceCount,
        totalCents: plan.totalCents,
        allocationCount,
        reservedUnits: allocation.reservedUnits,
        settlement,
        fee,
        shipment,
      };
      await storeCycle(cycleId, plan, result, observer);
      const report = await publishReport(plan, result, observer, options.reportEvery, signal);
      completedCycles += 1;
      lastCycle = cycleId;
      RequestTelemetry.capturePayload('dispatch-complete', { result, report, request });
      return { ...result, ...report };
    } catch (error) {
      RequestTelemetry.capturePayload('dispatch-error', { cycleId, error: error.message, request });
      throw error;
    } finally {
      running = false;
    }
  }

  return {
    start,
    stop,
    run,
    getSnapshot() {
      return {
        active,
        running,
        completedCycles,
        lastCycle,
        lastUpdate,
        options: { ...options },
      };
    },
  };
}
