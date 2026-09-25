export function createNotificationModel(clock) {
  var items = [];
  var nextId = 1;

  function add(message, options = {}) {
    var id = nextId;
    nextId += 1;
    var title = options.title == null ? '' : options.title;
    var item = {
      id,
      title,
      message,
      createdAt: clock.now(),
      expiresAt: options.duration === null ? null : clock.now() + (options.duration ?? 5000),
    };
    items = [...items, item];
    return id;
  }

  function remove(id) {
    var previousCount = items.length;
    items = items.filter(item => item.id !== id);
    return items.length !== previousCount;
  }

  function visible() {
    var now = clock.now();
    items = items.filter(item => item.expiresAt === null || item.expiresAt > now);
    return items.map(item => ({ ...item }));
  }

  return { add, remove, visible };
}
