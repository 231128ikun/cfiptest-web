// Shared form normalization for values that may also arrive from saved settings.

export function boundedNumber(value, {
    min = -Infinity,
    max = Infinity,
    integer = false,
    emptyValue = 0,
} = {}) {
    const raw = String(value ?? '').trim();
    if (!raw) return { value: emptyValue, changed: false, empty: true };
    const parsed = Number(raw);
    const candidate = Number.isFinite(parsed) ? parsed : emptyValue;
    const normalized = integer ? Math.trunc(candidate) : candidate;
    const bounded = Math.min(max, Math.max(min, normalized));
    return { value: bounded, changed: String(value) !== String(bounded), empty: false };
}
