import { TransactionManager } from '../../lib/parcel-kit.js';

function validateOrder(order) {
  if (!order || typeof order.id !== 'string' || !order.id.trim()) {
    throw new TypeError('Order ID is required');
  }
  if (!Array.isArray(order.lines) || order.lines.length === 0) {
    throw new TypeError('At least one order line is required');
  }
  for (const line of order.lines) {
    if (!line.sku || !Number.isInteger(line.quantity) || line.quantity <= 0) {
      throw new TypeError('Invalid order line');
    }
    if (!Number.isFinite(line.unitPrice) || line.unitPrice < 0) {
      throw new TypeError('Invalid unit price');
    }
  }
}

function buildSummary(order) {
  const lines = order.lines.map(line => ({
    sku: line.sku,
    quantity: line.quantity,
    amount: Math.round(line.unitPrice * line.quantity * 100),
  }));
  return {
    id: order.id,
    customerId: order.customerId,
    lines,
    totalCents: lines.reduce((total, line) => total + line.amount, 0),
  };
}

export async function placeOrder(order, notify) {
  validateOrder(order);
  const summary = buildSummary(order);
  const transaction = TransactionManager.open('orders');
  try {
    transaction.save('order:' + order.id, summary);
    for (const line of summary.lines) {
      transaction.save('reservation:' + order.id + ':' + line.sku, {
        orderId: order.id,
        sku: line.sku,
        quantity: line.quantity,
      });
    }
    await transaction.commit();
  } catch (error) {
    await transaction.rollback();
    throw error;
  }
  // A notification failure must not roll back an already committed order.
  await notify({ type: 'order-placed', orderId: order.id });
  return summary;
}
