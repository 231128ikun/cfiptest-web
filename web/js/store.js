// store.js —— 前端状态层
//
// 存在的理由：改造前，结构化数据被 stringify 进 textarea，再用正则读回来
// （app.js 的 parseCleanedToTargets）。那个正则不做校验，用户手改出错一行就会
// 产出 { ip: "1.2.3", port: NaN } 并一路送到后端。而「筛选」是把结果写回
// textarea，原始数据当场丢失，于是无法「清除筛选」回到全量。
//
// 这里把两件事分开：
//   - workspace / candidates / results 是唯一数据源，只增删改，不被视图污染；
//   - 筛选只产生派生视图（visibleWorkspace），原数组不动 → 清除筛选即可回退。
//
// 后续的候选区、组合筛选、配额都建立在这个前提上。

import { normalizeIPFormat, parseFilterExpression, lineMatchesFilter, getInputStats } from './input.js';

/** 把 {ip, port} 渲染成规范文本行；IPv6 加方括号。 */
export function targetToLine({ ip, port }) {
    if (!port) return ip;
    return ip.includes(':') ? `[${ip}]:${port}` : `${ip}:${port}`;
}

/**
 * 把一行文本解析成 {ip, port}；无法识别或端口非法时返回 null。
 *
 * 先过 normalizeIPFormat（它认得中文冒号、空格分隔、CSV 首列等脏格式），
 * 再从规范化结果里取值——因此这里只需处理 "ip:port" 与 "[v6]:port" 两种形状。
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

    const idx = normalized.lastIndexOf(':');
    if (idx <= 0) return null;
    const ip = normalized.slice(0, idx);
    const port = Number(normalized.slice(idx + 1));
    if (!Number.isInteger(port) || port < 1 || port > 65535) return null;
    return { ip, port };
}

/** 目标去重键。同 IP 不同端口是两个目标，故键含端口。 */
const targetKey = t => `${t.ip}|${t.port}`;

/** 结果去重键，与 targetKey 同构，便于结果与目标互相对应。 */
const resultKey = r => `${r.ip}|${r.port}`;

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
        const key = targetKey(target);
        if (seen.has(key)) { dupCount++; continue; }
        seen.add(key);
        targets.push(target);
    }
    return { targets, invalidCount, dupCount };
}

/* ---------------- Store ---------------- */

const listeners = new Set();

function notify() {
    for (const fn of listeners) fn(store);
}

/** 订阅状态变化，返回取消订阅函数。 */
export function subscribe(fn) {
    listeners.add(fn);
    return () => listeners.delete(fn);
}

export const store = {
    mode: 'proxy',        // 'proxy' | 'official'
    workspace: [],        // 工作区目标（结构化，已去重）
    workspaceFilter: '',  // 当前筛选表达式；只影响派生视图，不改 workspace
    filterMode: 'keep',   // 'keep' | 'remove'
    candidates: [],       // 候选区（阶段 2 的唯一输入源，已去重）
    results: [],          // 测试结果
};

/* ---------------- 模式 ---------------- */

export function setMode(mode) {
    if (mode !== 'proxy' && mode !== 'official') return;
    if (store.mode === mode) return;
    store.mode = mode;
    notify();
}

/* ---------------- 工作区 ---------------- */

/** 用文本整体替换工作区（粘贴框内容变化时调用）。 */
export function setWorkspaceFromText(rawText) {
    const { targets, invalidCount, dupCount } = parseLines(rawText);
    store.workspace = targets;
    notify();
    return { count: targets.length, invalidCount, dupCount };
}

/**
 * 往工作区追加目标（远程 TXT 导入、本地文件上传、官方段展开共用）。
 * 与已有内容一并去重，因此重复导入同一份列表不会让工作区膨胀。
 */
export function addToWorkspace(targets) {
    const seen = new Set(store.workspace.map(targetKey));
    let added = 0;
    let dupCount = 0;
    for (const t of targets) {
        if (!t) continue;
        const key = targetKey(t);
        if (seen.has(key)) { dupCount++; continue; }
        seen.add(key);
        store.workspace.push(t);
        added++;
    }
    if (added || dupCount) notify();
    return { added, dupCount };
}

/** 从文本追加到工作区（导入路径的便捷包装）。 */
export function addToWorkspaceFromText(rawText) {
    const { targets, invalidCount } = parseLines(rawText);
    const { added, dupCount } = addToWorkspace(targets);
    return { added, dupCount, invalidCount };
}

export function clearWorkspace() {
    if (!store.workspace.length && !store.workspaceFilter) return;
    store.workspace = [];
    store.workspaceFilter = '';
    notify();
}

/**
 * 设置筛选表达式。注意：不动 store.workspace——这正是「清除筛选」
 * 能回到全量的原因，也是旧实现（写回 textarea）做不到的。
 */
export function setWorkspaceFilter(expression, mode = 'keep') {
    store.workspaceFilter = String(expression || '');
    store.filterMode = mode === 'remove' ? 'remove' : 'keep';
    notify();
}

export function clearWorkspaceFilter() {
    if (!store.workspaceFilter) return;
    store.workspaceFilter = '';
    store.filterMode = 'keep';
    notify();
}

/** 筛选表达式是否合法；空表达式视为「无筛选」而非「无效」。 */
export function filterIsValid() {
    if (!store.workspaceFilter.trim()) return true;
    return parseFilterExpression(store.workspaceFilter) !== null;
}

/**
 * 工作区的可见视图（派生，不缓存——工作区规模是几千行量级，
 * 每次重算的成本远低于维护缓存失效的复杂度）。
 * 表达式无效时退化为全量，避免用户打字中途界面突然空掉。
 */
export function visibleWorkspace() {
    const expr = store.workspaceFilter.trim();
    if (!expr) return store.workspace;
    const criteria = parseFilterExpression(expr);
    if (!criteria) return store.workspace;
    return store.workspace.filter(t => {
        const matched = lineMatchesFilter(targetToLine(t), criteria);
        return store.filterMode === 'keep' ? matched : !matched;
    });
}

/** 工作区统计：可见数 / 总数 / 端口与 IP 类型分布。 */
export function workspaceStats() {
    const visible = visibleWorkspace();
    const stats = getInputStats(visible.map(targetToLine));
    return {
        ...stats,
        visible: visible.length,
        total: store.workspace.length,
        filtered: visible.length !== store.workspace.length,
    };
}

/* ---------------- 候选区 ---------------- */

/**
 * 把工作区当前【可见】行追加进候选区并去重。
 * 用可见行而非全量，是为了让「筛选 → 追加」成为一条自然的工作流。
 */
export function appendVisibleToCandidates() {
    return addToCandidates(visibleWorkspace());
}

export function addToCandidates(targets) {
    const seen = new Set(store.candidates.map(targetKey));
    let added = 0;
    let dupCount = 0;
    for (const t of targets) {
        if (!t) continue;
        const key = targetKey(t);
        if (seen.has(key)) { dupCount++; continue; }
        seen.add(key);
        store.candidates.push(t);
        added++;
    }
    notify();
    return { added, dupCount };
}

export function clearCandidates() {
    if (!store.candidates.length) return;
    store.candidates = [];
    notify();
}

/** 候选区快照，供发起测试用（复制一份，避免调用方改到内部状态）。 */
export function candidateTargets() {
    return store.candidates.map(t => ({ ip: t.ip, port: t.port }));
}

/* ---------------- 结果 ---------------- */

export function addResult(result) {
    store.results.push(result);
    notify();
}

/**
 * 回填测速结果。测速阶段只回传 ip/port/速度，需要按键找到原结果补字段。
 * 找不到对应项时作为新结果收下——总比静默丢弃一次真实测速好。
 */
export function updateSpeed(partial) {
    const key = resultKey(partial);
    const target = store.results.find(r => resultKey(r) === key);
    if (target) {
        target.downloadSpeedKBs = partial.downloadSpeedKBs;
    } else {
        store.results.push(partial);
    }
    notify();
}

export function clearResults() {
    if (!store.results.length) return;
    store.results = [];
    notify();
}

/** 供测试与调试：把状态恢复到初始值。 */
export function resetStore() {
    store.mode = 'proxy';
    store.workspace = [];
    store.workspaceFilter = '';
    store.filterMode = 'keep';
    store.candidates = [];
    store.results = [];
    notify();
}
