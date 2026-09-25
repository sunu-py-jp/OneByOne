import { ReportWriter } from '../../lib/parcel-kit.js';

function invoiceRows(invoice) {
  const rows = [];
  for (const item of invoice.items) {
    rows.push({
      invoiceId: invoice.id,
      customerId: invoice.customerId,
      sku: item.sku,
      quantity: item.quantity,
      amountCents: Math.round(item.price * item.quantity * 100),
    });
  }
  return rows;
}

function validateInvoices(invoices) {
  for (const invoice of invoices) {
    if (!invoice.id || !Array.isArray(invoice.items)) {
      throw new TypeError('Invalid invoice');
    }
  }
}

export async function exportInvoices(invoices, progress) {
  validateInvoices(invoices);
  const report = ReportWriter.open('invoices');
  let written = 0;
  let totalCents = 0;
  try {
    for (const invoice of invoices) {
      const rows = invoiceRows(invoice);
      for (const row of rows) {
        report.add(row);
        written += 1;
        totalCents += row.amountCents;
      }
      // The count means durable rows, not rows waiting in a local buffer.
      await progress.saved({ invoiceId: invoice.id, rows: written });
    }
    const result = {
      reportId: report.id,
      invoiceCount: invoices.length,
      rowCount: written,
      totalCents,
    };
    await progress.completed(result);
    return result;
  } finally {
    report.finish();
  }
}
