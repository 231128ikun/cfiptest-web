// api.js —— 后端 API 客户端：fetch 封装 + SSE 事件订阅

async function postJSON(url, body) {
    const resp = await fetch(url, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
    });
    const data = await resp.json().catch(() => ({}));
    if (!resp.ok) {
        throw new Error(data.error || `HTTP ${resp.status}`);
    }
    return data;
}

export async function fetchConfig() {
    const resp = await fetch('/api/config');
    if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
    return resp.json();
}

/**
 * 启动延迟测试。
 * targets 为 [{ip, port}]，由候选区提供（网段已在导入阶段展开）。
 * options 可部分提供，缺的字段后端补默认值。
 */
export function startLatencyTest(targets, options, { enableSpeed = false, speedOptions } = {}) {
    return postJSON('/api/task/latency', { targets, options, enableSpeed, speedOptions });
}

/** 官方 IP 段 + 各抽样模式的预估数量；n 为「每 /24 取 N 个」模式的 N */
export async function fetchOfficialRanges(n) {
    const query = n > 0 ? `?n=${n}` : '';
    const resp = await fetch(`/api/official-ranges${query}`);
    if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
    return resp.json();
}

/**
 * 导入远程 IP 列表。
 * 由后端代取而非前端 fetch：订阅地址基本都不带 CORS 头，浏览器直连会被拦。
 */
export function importRemote(url, { sampleMode = 'one', sampleN = 1 } = {}) {
    return postJSON('/api/import/remote', { url, sampleMode, sampleN });
}

/**
 * 解析一段 IP 文本，返回展开后的 [{ip, port}]。
 * 用后端解析而非前端 store.parseLines，是因为文本里可能混写 CIDR 网段——
 * 网段的抽样算法只在 engine 里有一份（已有单测），不在 JS 里重复实现。
 */
export function importText(text, { sampleMode = 'one', sampleN = 1 } = {}) {
    return postJSON('/api/import/text', { text, sampleMode, sampleN });
}

/** 启动测速；targets 为从结果中挑选的子集 */
export function startSpeedTest(targets, options) {
    return postJSON('/api/task/speed', { targets, options });
}

export function stopTask(taskId) {
    return postJSON('/api/task/stop', { taskId: taskId || '' });
}

/**
 * 订阅 SSE 事件流。
 * handlers: { onResult, onProgress, onSpeed, onDone, onError, onOpen }
 * onDone 收到 (message, reason)，reason ∈ completed | limit | stopped：
 * 后两者是正常收工，界面不该按错误处理。
 * 返回 EventSource（可 close()）。
 */
export function subscribeEvents(handlers) {
    const es = new EventSource('/api/task/events');

    es.onopen = () => handlers.onOpen?.();
    es.onerror = () => handlers.onError?.('事件流连接中断');

    es.addEventListener('result', e => handlers.onResult?.(JSON.parse(e.data).result));
    es.addEventListener('progress', e => handlers.onProgress?.(JSON.parse(e.data).progress));
    es.addEventListener('speed', e => handlers.onSpeed?.(JSON.parse(e.data).result));
    es.addEventListener('done', e => {
        const data = JSON.parse(e.data);
        handlers.onDone?.(data.message, data.reason || 'completed');
    });
    es.addEventListener('error', e => {
        const data = e.data ? JSON.parse(e.data) : {};
        handlers.onError?.(data.message || '未知错误');
    });

    return es;
}
