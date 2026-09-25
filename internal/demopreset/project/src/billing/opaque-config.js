import { ClientOptions } from '../../lib/parcel-kit.js';

// The host supplies extension settings. Its timeout units, null behavior and extra keys
// are intentionally not specified in this plugin's interface documentation.
export function configureBillingExtension(hostSettings, extension) {
  if (!hostSettings || typeof hostSettings !== 'object') {
    throw new TypeError('Host settings are required');
  }
  const resolved = ClientOptions.resolve(hostSettings);
  extension.configure(resolved);
  return {
    name: extension.name,
    ready: extension.isReady(),
  };
}

export function describeBillingExtension(extension) {
  return {
    name: extension.name,
    provider: 'external-host',
    settingsManagedBy: 'host-administrator',
  };
}
