function normalizeLine(line) {
  var quantity = Number(line.quantity);
  if (!Number.isInteger(quantity) || quantity <= 0) {
    throw new TypeError('Invalid quantity');
  }
  var title = line.title == null ? '' : line.title;
  return {
    sku: line.sku,
    title,
    quantity,
    amountCents: Math.round(line.price * quantity * 100),
  };
}

export function orderSummary(order) {
  var lines = order.lines.map(normalizeLine);
  var totalCents = 0;
  var totalUnits = 0;
  for (const line of lines) {
    totalCents += line.amountCents;
    totalUnits += line.quantity;
  }
  var note = order.note == null ? '' : order.note;
  return {
    id: order.id,
    note,
    lines,
    totalCents,
    totalUnits,
    isEmpty: lines.length === 0,
  };
}
