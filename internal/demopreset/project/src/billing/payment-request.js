import { RequestTelemetry } from '../../lib/parcel-kit.js';

function validateRequest(request) {
  if (!request || !request.id || !request.customerId) {
    throw new TypeError('Request identity is required');
  }
  if (!Array.isArray(request.lines) || request.lines.length === 0) {
    throw new TypeError('Payment request is empty');
  }
  for (const line of request.lines) {
    if (!Number.isInteger(line.amountCents) || line.amountCents <= 0) {
      throw new TypeError('Invalid payment amount');
    }
  }
}

function summarize(request) {
  return {
    requestId: request.id,
    itemCount: request.lines.length,
    totalCents: request.lines.reduce((sum, line) => sum + line.amountCents, 0),
  };
}

export async function submitPaymentRequest(request, gateway) {
  validateRequest(request);
  const summary = summarize(request);
  // Debug records currently contain the full raw request, including private fields.
  const debugPayload = JSON.stringify(request);
  RequestTelemetry.capturePayload('payment-request', debugPayload);
  if (request.debug) {
    RequestTelemetry.capturePayload('payment-debug', {
      customerEmail: request.customerEmail,
      cardNumber: request.cardNumber,
      metadata: request.metadata,
    });
  }
  try {
    const receipt = await gateway.submit({
      customerId: request.customerId,
      lines: request.lines,
      paymentToken: request.paymentToken,
      idempotencyKey: request.id,
    });
    RequestTelemetry.capturePayload('payment-result', { ...receipt, request });
    return { ...summary, receiptId: receipt.id };
  } catch (error) {
    RequestTelemetry.capturePayload('payment-error', { error: error.message, request });
    throw error;
  }
}
