import { ClientOptions } from '../../lib/parcel-kit.js';

function readNumericSetting(value, name) {
  if (value === undefined || value === null) return value;
  const number = Number(value);
  if (!Number.isFinite(number) || number < 0) {
    throw new TypeError(name + ' must be a non-negative number');
  }
  return number;
}

function retryCount(value) {
  if (value === undefined) return 2;
  const count = Number(value);
  if (!Number.isInteger(count) || count < 0 || count > 5) {
    throw new TypeError('Retries must be between zero and five');
  }
  return count;
}

export function resolveRuntimeOptions(settings) {
  const timeout = readNumericSetting(settings.timeoutSeconds, 'Timeout');
  const retries = retryCount(settings.retries);
  const label = settings.label == null ? '' : settings.label;
  const effective = ClientOptions.resolve({
    timeoutSeconds: timeout,
    retries,
    label,
  });
  return {
    ...effective,
    environment: settings.environment ?? 'development',
    features: {
      dryRun: settings.dryRun === true,
      compact: settings.compact !== false,
    },
  };
}
