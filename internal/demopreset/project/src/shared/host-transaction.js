import { TransactionManager } from '../../lib/parcel-kit.js';

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
