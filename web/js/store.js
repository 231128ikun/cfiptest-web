// store.js —— 前端文本解析与模式状态
//
// 历史上这里还带一套集中式状态层（workspace / candidates / results / 订阅），
// 但 app.js 早已改为直接管理候选数组，结果集由 ResultTable 持有，
// 集中式 Store 不再被任何页面使用，已整体移除，仅保留实际用到的部分。

import { normalizeIPFormat } from './input.js';

/** 把 {ip, port} 渲染成规范文本行；IPv6 加方括号。 */
export function targetToLine({ ip, port }) {
    if (!port) return ip;
    return ip.includes(':') ? `[${ip}]:${port}` : `${ip}:${port}`;
}

/**
 * 把一行文本解析成 {ip, port}；无法识别或端口非法时返回 null。
 *
 * 先过 normalizeIPFormat（它认得中文冒号、空格分隔、CSV 首列等格式），
 * 再从规范化结果里取值——因此这里只需处理 "ip:port" 与 "[v6]:port" 两种形态。
 * 关键是绝不返回 NaN 端口：旧实现正是靠 parseInt 静默失败把笔误送进了请求体。
 */
export function lineToTarget(line) {
    const normalized = normalizeIPFormat(line);
    if (!normalized) return null;

    const v6 = normalized.match(/^\[([0-9a-fA-F:]+)\]:(\d+)$/);
    if (v6) return { ip: v6[1], port: Number(v6[2]) };

    // 没写端口：保留 port=0，执行阶段再根据 TLS 规则补成 443/80。
    if (/^\d{1,3}(?:\.\d{1,3}){3}$/.test(normalized)) {
        return { ip: normalized, port: 0 };
    }
    if (/^[0-9a-fA-F:]+$/.test(normalized) && normalized.includes(':')) {
        return { ip: normalized, port: 0 };
    }

    // CSV 导入展示为 "IP PORT COUNTRY"，解析目标时保留前两个字段。
    const fields = normalized.split(/\s+/);
    if (fields.length >= 2 && /^\d{1,3}(?:\.\d{1,3}){3}$/.test(fields[0])) {
        const port = Number(fields[1]);
        if (Number.isInteger(port) && port >= 1 && port <= 65535) return { ip: fields[0], port };
    }

    const idx = normalized.lastIndexOf(':');
    if (idx <= 0) return null;
    const ip = normalized.slice(0, idx);
    const port = Number(normalized.slice(idx + 1));
    if (!Number.isInteger(port) || port < 1 || port > 65535) return null;
    return { ip, port };
}

/** 目标/结果统一去重键：`ip|port`；缺端口按 0 处理。 */
export const entryKey = item => `${item.ip}|${item.port || 0}`;

/**
 * 从多行文本解析出目标列表，顺带统计丢弃与重复数量。
 * 解析与去重都在这里做，所以 UI 层不必再有「整理」「仅去重」两个按钮。
 */
export function parseLines(rawText) {
    const lines = String(rawText || '').split('\n');
    const seen = new Set();
    const targets = [];
    let invalidCount = 0;
    let dupCount = 0;

    for (const line of lines) {
        const trimmed = line.trim();
        if (!trimmed || trimmed.startsWith('#')) continue;
        const target = lineToTarget(trimmed);
        if (!target) { invalidCount++; continue; }
        const key = entryKey(target);
        if (seen.has(key)) { dupCount++; continue; }
        seen.add(key);
        targets.push(target);
    }
    return { targets, invalidCount, dupCount };
}

/* ---------------- 模式 ---------------- */

export const store = { mode: 'proxy' }; // 'proxy' | 'official'

export function setMode(mode) {
    if (mode !== 'proxy' && mode !== 'official') return;
    store.mode = mode;
}