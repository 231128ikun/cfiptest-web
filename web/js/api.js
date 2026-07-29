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

/** 启动延迟测试；targets 为 [{ip, port}]，options 可部分提供（后端补默认值） */
export function startLatencyTest(targets, options) {
    return postJSON('/api/task/latency', { targets, options });
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
 * 返回 EventSource（可 close()）。
 */
export function subscribeEvents(handlers) {
    const es = new EventSource('/api/task/events');

    es.onopen = () => handlers.onOpen?.();
    es.onerror = () => handlers.onError?.('事件流连接中断');

    es.addEventListener('result', e => handlers.onResult?.(JSON.parse(e.data).result));
    es.addEventListener('progress', e => handlers.onProgress?.(JSON.parse(e.data).progress));
    es.addEventListener('speed', e => handlers.onSpeed?.(JSON.parse(e.data).result));
    es.addEventListener('done', e => handlers.onDone?.(JSON.parse(e.data).message));
    es.addEventListener('error', e => {
        const data = e.data ? JSON.parse(e.data) : {};
        handlers.onError?.(data.message || '未知错误');
    });

    return es;
}
