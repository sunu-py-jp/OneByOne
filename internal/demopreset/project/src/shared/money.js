const SUPPORTED_CURRENCIES = new Set(['JPY', 'USD', 'EUR']);

export function currencyScale(currency) {
  if (!SUPPORTED_CURRENCIES.has(currency)) throw new TypeError('Unsupported currency');
  return currency === 'JPY' ? 1 : 100;
}

export function toMinorUnits(amount, currency) {
  if (!Number.isFinite(amount)) throw new TypeError('Amount must be finite');
  return Math.round(amount * currencyScale(currency));
}

export function allocateMinorUnits(total, weights) {
  if (!Number.isInteger(total) || total < 0) throw new RangeError('Invalid total');
  if (weights.length === 0) return [];
  if (weights.some(weight => !Number.isFinite(weight) || weight <= 0)) {
    throw new RangeError('Invalid weights');
  }
  const sum = weights.reduce((value, weight) => value + weight, 0);
  const allocations = weights.map(weight => Math.floor(total * weight / sum));
  let remaining = total - allocations.reduce((value, amount) => value + amount, 0);
  for (let index = 0; remaining > 0; index = (index + 1) % allocations.length) {
    allocations[index] += 1;
    remaining -= 1;
  }
  return allocations;
}
