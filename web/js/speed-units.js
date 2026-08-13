// 自动维护界面使用 Mbps；后端继续沿用现有的 kB/s 数值，以兼容旧任务和 API。
export const SPEED_KBS_PER_MBPS = 1_000_000 / 8 / 1024;

function positiveNumber(value) {
    const number = Number(value);
    return Number.isFinite(number) && number > 0 ? number : 0;
}

export function mbpsToSpeedKBs(value) {
    return positiveNumber(value) * SPEED_KBS_PER_MBPS;
}

export function speedKBsToMbps(value) {
    return positiveNumber(value) / SPEED_KBS_PER_MBPS;
}

export function formatSpeedMbps(value) {
    const mbps = speedKBsToMbps(value);
    return mbps > 0 ? String(Number(mbps.toFixed(6))) : '';
}