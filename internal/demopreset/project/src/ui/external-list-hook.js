import { RecordDirectory } from '../../lib/parcel-kit.js';

// The desktop host invokes this plugin synchronously during its render pass.
// The host is distributed separately and rejects Promise return values.
export function provideOrderChoices(customerId) {
  const records = RecordDirectory.listAll({ customerId });
  return records.filter(order => order.status !== 'cancelled')
    .sort((left, right) => right.updatedAt - left.updatedAt)
    .map(order => ({
      value: order.id,
      label: order.id + ' (' + order.totalCents + ')',
      disabled: order.status === 'shipped',
    }));
}

export function registerOrderChoiceProvider(host) {
  // The host owns registration lifetime. Its implementation is unavailable here.
  host.register('order-choices', provideOrderChoices);
  return { name: 'order-choices', version: 1 };
}
