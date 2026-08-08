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

// 范围输入统一解析：空 → 不限制；"a~b" → 双向区间；
// 单值按 singleBias 解释：延迟(max) 表示上限，速度(min) 表示下限。
export function parseRangeInput(raw, { singleBias = 'max' } = {}) {
    const v = String(raw ?? '').trim();
    if (!v) return {};
    const parts = v.split(/[~～]/);
    if (parts.length >= 2) {
        const min = parseFloat(parts[0]);
        const max = parseFloat(parts[1]);
        return {
            min: Number.isFinite(min) && min > 0 ? min : 0,
            max: Number.isFinite(max) && max > 0 ? max : 0,
        };
    }
    const n = parseFloat(v);
    if (!Number.isFinite(n) || n <= 0) return {};
    return singleBias === 'min' ? { min: n } : { max: n };
}
