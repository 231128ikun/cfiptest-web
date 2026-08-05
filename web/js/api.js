// api.js —— 后端 API 客户端：fetch 封装 + SSE 事件订阅

async function postJSON(url, body, method = 'POST') {
    const options = { method, headers: { 'Content-Type': 'application/json' } };
    if (method !== 'GET') options.body = JSON.stringify(body); // GET 带 body 会被浏览器拒绝
    const resp = await fetch(url, options);
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

export function saveConfig(config) { return postJSON('/api/config', config, 'PUT'); }
export function saveSettings(settings) { return postJSON('/api/settings', settings, 'PUT'); }

/**
 * 启动延迟测试。
 * targets 为 [{ip, port}]，由候选区提供（网段已在导入阶段展开）。
 * options 可部分提供，缺的字段后端补默认值。
 */
export function startLatencyTest(targets, options, { enableSpeed = false, speedOptions } = {}) {
    return postJSON('/api/task/latency', { targets, options, enableSpeed, speedOptions });
}

/** 官方 IP 段 + 各抽样模式的预估数量；n 为「每 /24 取 N 个」模式的 N */
export async function fetchOfficialRanges(n, { refresh = false } = {}) {
	const params = new URLSearchParams();
	if (n > 0) params.set('n', n);
	if (refresh) params.set('refresh', '1');
	const query = params.size ? `?${params}` : '';
    const resp = await fetch(`/api/official-ranges${query}`);
    if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
    return resp.json();
}

/**
 * 导入远程 TXT / CSV IP 列表。
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

// ---- 自动化（维护任务 / IP 库 / 运行历史） ----

export function fetchTasks() { return postJSON('/api/auto/tasks', {}, 'GET'); }
export function saveTasks(tasks) { return postJSON('/api/auto/tasks', { tasks }, 'PUT'); }
export function validateTask(task) { return postJSON('/api/auto/tasks/validate', task); }
export function uploadAutoInput(name, text) { return postJSON('/api/auto/input/upload', { name, text }); }

export function fetchLibraries() { return postJSON('/api/auto/libraries', {}, 'GET'); }
export function createLibrary(name) { return postJSON('/api/auto/libraries', { name }); }
export function renameLibrary(id, name) { return postJSON('/api/auto/libraries/rename', { id, name }); }
export function deleteLibrary(id) { return postJSON('/api/auto/libraries/delete', { id }); }
export function clearLibrary(id) { return postJSON('/api/auto/libraries/clear', { id, confirm: true }); }

export function fetchAutoLibrary(params = {}) {
    const query = new URLSearchParams();
    if (params.lib) query.set('lib', params.lib);
    if (params.status) query.set('status', params.status);
    if (params.country) query.set('country', params.country);
    if (params.q) query.set('q', params.q);
    if (params.offset != null) query.set('offset', params.offset);
    if (params.limit != null) query.set('limit', params.limit);
    const qs = query.size ? `?${query}` : '';
    return postJSON(`/api/auto/library${qs}`, {}, 'GET');
}

export function importAutoLibrary({ lib, targets, text, source, results }) {
    return postJSON('/api/auto/library/import', { lib, targets, text, source, results });
}
export function removeAutoLibrary(lib, keys) { return postJSON('/api/auto/library/remove', { lib, keys }); }

export function runAuto(taskId) { return postJSON('/api/auto/run', { taskId }); }

// 调试日志
export function fetchLog(lines = 200) { return postJSON(`/api/log?lines=${lines}`, {}, 'GET'); }
export function clearLog() { return postJSON('/api/log/clear', {}); }

/** 下载订阅输出文件（path 相对 data 目录） */
export function autoOutputUrl(path) {
    return `/api/auto/output?path=${encodeURIComponent(path)}`;
}

/** 启动测速；targets 为从结果中挑选的子集 */
export function startSpeedTest(targets, options) {
    return postJSON('/api/task/speed', { targets, options });
}

export function stopTask(taskId) {
    return postJSON('/api/task/stop', { taskId: taskId || '' });
}

export async function fetchTaskStatus() {
    const resp = await fetch('/api/task/status');
    if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
    return resp.json();
}

/**
 * 订阅 SSE 事件流。
 * handlers: { onResult, onProgress, onSpeed, onAuto, onDone, onError, onOpen }
 * onDone 收到 (message, reason)，reason ∈ completed | limit | stopped：
 * 后两者是正常收工，界面不该按错误处理。
 * 返回 EventSource（可 close()）。
 */
export function subscribeEvents(handlers) {
    const es = new EventSource('/api/task/events');

    es.onopen = () => handlers.onOpen?.();
    es.onerror = event => {
        if (event?.data) {
            const data = JSON.parse(event.data);
            handlers.onError?.(data.message || '未知错误');
            return;
        }
        handlers.onDisconnect?.();
    };

    es.addEventListener('result', e => handlers.onResult?.(JSON.parse(e.data).result));
    es.addEventListener('progress', e => handlers.onProgress?.(JSON.parse(e.data).progress));
    es.addEventListener('speed', e => handlers.onSpeed?.(JSON.parse(e.data).result));
    es.addEventListener('auto', e => handlers.onAuto?.(JSON.parse(e.data).message));
    es.addEventListener('done', e => {
        const data = JSON.parse(e.data);
        handlers.onDone?.(data.message, data.reason || 'completed');
    });
    return es;
}
