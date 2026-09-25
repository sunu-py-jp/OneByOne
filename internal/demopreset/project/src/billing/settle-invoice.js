import { InvoiceService } from '../../lib/parcel-kit.js';

function validateInvoice(invoice) {
  if (!invoice || !invoice.id) throw new TypeError('Invoice was not found');
  if (!Number.isInteger(invoice.totalCents) || invoice.totalCents <= 0) {
    throw new TypeError('Invoice total is invalid');
  }
  if (invoice.status === 'cancelled') throw new Error('Invoice is cancelled');
}

function receiptSummary(invoice, receipt) {
  return {
    invoiceId: invoice.id,
    customerId: invoice.customerId,
    receiptId: receipt.id,
    totalCents: invoice.totalCents,
    status: 'settled',
  };
}

export async function settleInvoice(invoiceId) {
  if (typeof invoiceId !== 'string' || !invoiceId.trim()) {
    throw new TypeError('Invoice ID is required');
  }
  return new Promise((resolve, reject) => {
    InvoiceService.lookup(invoiceId, (lookupError, invoice) => {
      if (lookupError) {
        reject(lookupError);
        return;
      }
      try {
        validateInvoice(invoice);
      } catch (error) {
        reject(error);
        return;
      }
      InvoiceService.authorize(invoice, (authorizationError, authorization) => {
        if (authorizationError) {
          reject(authorizationError);
          return;
        }
        InvoiceService.capture(authorization, (captureError, receipt) => {
          if (captureError) {
            InvoiceService.void(authorization.id, () => {
              reject(captureError);
            });
            return;
          }
          resolve(receiptSummary(invoice, receipt));
        });
      });
    });
  });
}
