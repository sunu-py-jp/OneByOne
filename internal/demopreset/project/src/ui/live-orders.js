import { EventBus } from '../../lib/parcel-kit.js';

export function createLiveOrders(view, scope) {
  let active = false;
  let generation = 0;
  const displayed = new Map();

  function render() {
    const rows = [...displayed.values()]
      .filter(order => !order.archived)
      .sort((left, right) => right.updatedAt - left.updatedAt);
    view.render(rows);
    view.setCount(rows.length);
  }

  function applyOrder(order, expectedGeneration) {
    if (!active || generation !== expectedGeneration) return;
    if (!order || !order.id) return;
    displayed.set(order.id, { ...order });
    render();
  }

  function removeOrder(id, expectedGeneration) {
    if (!active || generation !== expectedGeneration) return;
    displayed.delete(id);
    render();
  }

  function start(initialOrders = []) {
    if (active) return;
    active = true;
    generation += 1;
    const currentGeneration = generation;
    displayed.clear();
    for (const order of initialOrders) displayed.set(order.id, { ...order });
    try {
      EventBus.on(scope, 'order:changed', order => {
        applyOrder(order, currentGeneration);
      });
      EventBus.on(scope, 'order:removed', id => {
        removeOrder(id, currentGeneration);
      });
      render();
    } catch (error) {
      active = false;
      EventBus.clear(scope);
      throw error;
    }
  }

  function stop() {
    if (!active) return;
    active = false;
    generation += 1;
    EventBus.clear(scope);
    displayed.clear();
    view.setCount(0);
  }

  return { start, stop, isActive: () => active };
}
