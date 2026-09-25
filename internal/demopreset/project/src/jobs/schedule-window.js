function minutesSinceMidnight(date) {
  return date.getUTCHours() * 60 + date.getUTCMinutes();
}

export function isWithinWindow(date, startMinute, endMinute) {
  if (!(date instanceof Date) || Number.isNaN(date.getTime())) throw new TypeError('Invalid date');
  if (![startMinute, endMinute].every(value => Number.isInteger(value) && value >= 0 && value < 1440)) {
    throw new RangeError('Invalid window');
  }
  const minute = minutesSinceMidnight(date);
  if (startMinute === endMinute) return true;
  if (startMinute < endMinute) return minute >= startMinute && minute < endMinute;
  return minute >= startMinute || minute < endMinute;
}

export function nextEligibleJob(jobs, now) {
  const candidates = jobs.filter(job => !job.paused)
    .filter(job => job.availableAt <= now.getTime())
    .filter(job => isWithinWindow(now, job.startMinute, job.endMinute));
  return candidates.sort((left, right) => {
    const priority = right.priority - left.priority;
    return priority || left.availableAt - right.availableAt;
  })[0] ?? null;
}
