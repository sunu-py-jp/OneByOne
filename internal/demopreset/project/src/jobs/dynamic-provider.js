import { RecordDirectory } from '../../lib/parcel-kit.js';

// The external host chooses an operation and positional arguments from its own config.
// It can request listAll or count; the configuration schema is not part of this project.
export function executeDirectoryCommand(command) {
  if (!command || typeof command.operation !== 'string' || !Array.isArray(command.args)) {
    throw new TypeError('Invalid directory command');
  }
  const operation = RecordDirectory[command.operation];
  if (typeof operation !== 'function') throw new Error('Unknown directory operation');
  const result = operation(...command.args);
  return {
    commandId: command.id,
    result,
    executedAt: Date.now(),
  };
}

export function providerDescription() {
  return { kind: 'synchronous-directory', configurableOperations: true };
}
