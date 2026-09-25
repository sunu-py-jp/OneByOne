import { ReportWriter, RequestTelemetry } from '../../lib/parcel-kit.js';

function auditRow(settlement) {
  return {
    settlementId: settlement.id,
    invoiceId: settlement.invoiceId,
    amountCents: settlement.amountCents,
    status: settlement.status,
  };
}

export async function writeSettlementAudit(settlements, observer) {
  const report = ReportWriter.open('settlement-audit');
  let count = 0;
  try {
    for (const settlement of settlements) {
      if (settlement.status !== 'settled') continue;
      report.add(auditRow(settlement));
      count += 1;
      RequestTelemetry.capturePayload('settlement-audit-row', settlement);
    }
    // The observer may immediately read the report identified by this ID.
    await observer.ready({ reportId: report.id, count });
    return { reportId: report.id, count };
  } finally {
    report.finish();
  }
}
