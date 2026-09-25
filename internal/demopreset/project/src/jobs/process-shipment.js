import { WorkQueue } from '../../lib/parcel-kit.js';

function decodeJob(job) {
  if (!job || !job.id || !job.orderId) {
    throw new TypeError('Invalid shipment job');
  }
  if (!Array.isArray(job.parcels) || job.parcels.length === 0) {
    throw new TypeError('Shipment has no parcels');
  }
  return {
    id: job.id,
    orderId: job.orderId,
    parcels: job.parcels.map(parcel => ({
      code: parcel.code,
      weightGrams: parcel.weightGrams,
    })),
  };
}

function summarize(job, labels) {
  return {
    jobId: job.id,
    orderId: job.orderId,
    parcelCount: labels.length,
    trackingCodes: labels.map(label => label.trackingCode),
  };
}

export async function processNextShipment(carrier, shipmentStore) {
  const message = WorkQueue.take('shipments');
  if (!message) return null;
  // The old consumer acknowledged receipt before downstream work completed.
  WorkQueue.ack(message.id);
  try {
    const job = decodeJob(message);
    const labels = [];
    for (const parcel of job.parcels) {
      const label = await carrier.createLabel({
        orderId: job.orderId,
        parcel,
        idempotencyKey: job.id + ':' + parcel.code,
      });
      labels.push(label);
    }
    const result = summarize(job, labels);
    await shipmentStore.save(result);
    return result;
  } catch (error) {
    WorkQueue.fail(message.id, error.message);
    throw error;
  }
}
