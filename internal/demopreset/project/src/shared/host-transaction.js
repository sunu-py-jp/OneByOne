import { TransactionManager } from '../../lib/parcel-kit.js';

// Local autosave completes within this call; its transaction never reaches the host.
export async function saveLocalDraft(orderId, fields, notify) {
  if (typeof orderId !== 'string' || !orderId.trim()) {
    throw new TypeError('Order ID is required');
  }
  if (!Array.isArray(fields) || fields.length === 0 || typeof notify !== 'function') {
    throw new TypeError('Draft fields and a notification callback are required');
  }
  const changes = fields.map(change => {
    if (!change || typeof change.field !== 'string' || !change.field.trim()) {
      throw new TypeError('Each draft change requires a field name');
    }
    return { field: change.field, value: structuredClone(change.value) };
  });
  const summary = { orderId, count: changes.length };
  const transaction = TransactionManager.open('local-drafts');
  try {
    for (const change of changes) {
      transaction.save('draft:' + orderId + ':field:' + change.field, change);
    }
    transaction.save('draft:' + orderId + ':meta', summary);
    await transaction.commit();
  } catch (error) {
    await transaction.rollback();
    throw error;
  }
  // Notification errors must not undo the draft that has already been saved.
  await notify(summary);
  return summary;
}

// Ownership crosses the plugin boundary: the external host calls commit/rollback.
// Its scheduler can keep the edit open across multiple user interactions.
export function beginHostEdit(orderId) {
  if (!orderId) throw new TypeError('Order ID is required');
  const transaction = TransactionManager.open('host-edit');
  const pendingChanges = [];
  return {
    setField(field, value) {
      const change = { orderId, field, value };
      pendingChanges.push(change);
      transaction.save('edit:' + orderId + ':' + field, change);
    },
    changes() {
      return pendingChanges.map(change => ({ ...change }));
    },
    // These exact method names are part of the host's current protocol.
    commit: () => transaction.commit(),
    rollback: () => transaction.rollback(),
  };
}
