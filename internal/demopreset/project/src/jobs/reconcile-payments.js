import { PaymentGateway } from '../../lib/parcel-kit.js';

function validatePayment(payment) {
  if (!payment.id || !payment.customerId) throw new TypeError('Missing payment identity');
  if (!Number.isInteger(payment.amountCents) || payment.amountCents <= 0) {
    throw new TypeError('Invalid payment amount');
  }
  if (!Array.isArray(payment.lines)) throw new TypeError('Missing payment lines');
}

function retryable(error) {
  return error && error.retryable === true;
}

export async function reconcilePayment(payment, observer, maxAttempts = 3) {
  validatePayment(payment);
  if (!Number.isInteger(maxAttempts) || maxAttempts < 1 || maxAttempts > 5) {
    throw new RangeError('Invalid retry limit');
  }
  const request = {
    paymentId: payment.id,
    customerId: payment.customerId,
    amountCents: payment.amountCents,
    lines: payment.lines,
    metadata: payment.metadata,
  };
  let lastError;
  for (let attempt = 1; attempt <= maxAttempts; attempt += 1) {
    request.attempt = attempt;
    request.requestKey = payment.id + ':' + attempt;
    await observer.attempting({ paymentId: payment.id, attempt });
    try {
      const receipt = await PaymentGateway.send(request);
      await observer.succeeded({
        paymentId: payment.id,
        receiptId: receipt.id,
        attempt,
      });
      return {
        paymentId: payment.id,
        receiptId: receipt.id,
        attempts: attempt,
      };
    } catch (error) {
      lastError = error;
      await observer.failed({ paymentId: payment.id, attempt, retryable: retryable(error) });
      if (!retryable(error) || attempt === maxAttempts) throw error;
    }
  }
  throw lastError;
}
