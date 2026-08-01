import assert from 'node:assert/strict';

class FakeEventSource {
    static instance = null;

    constructor(url) {
        this.url = url;
        this.listeners = new Map();
        FakeEventSource.instance = this;
    }

    addEventListener(type, handler) {
        (this.listeners.get(type) || this.listeners.set(type, []).get(type)).push(handler);
    }

    emit(type, data) {
        const event = data === undefined ? { type } : { type, data: JSON.stringify(data) };
        if (type === 'open') this.onopen?.(event);
        else if (type === 'error') this.onerror?.(event);
        else (this.listeners.get(type) || []).forEach(handler => handler(event));
    }

    close() {}
}

globalThis.EventSource = FakeEventSource;
const { subscribeEvents } = await import('../web/js/api.js');

let pass = 0;
const check = (name, fn) => {
    try { fn(); pass++; console.log(`  ok  ${name}`); }
    catch (e) { console.log(`  FAIL ${name}\n       ${e.message}`); process.exitCode = 1; }
};

check('网络错误只触发断线回调，不触发任务错误', () => {
    const seen = [];
    subscribeEvents({
        onDisconnect: () => seen.push('disconnect'),
        onError: message => seen.push(`error:${message}`),
    });
    FakeEventSource.instance.emit('error');
    assert.deepEqual(seen, ['disconnect']);
});
check('服务端自定义 error 事件触发任务错误', () => {
    const seen = [];
    subscribeEvents({
        onDisconnect: () => seen.push('disconnect'),
        onError: message => seen.push(`error:${message}`),
    });
    FakeEventSource.instance.emit('error', { message: '任务失败' });
    assert.deepEqual(seen, ['error:任务失败']);
});
check('重连后 onopen 可以再次触发', () => {
    let opens = 0;
    subscribeEvents({ onOpen: () => { opens++; } });
    FakeEventSource.instance.emit('open');
    FakeEventSource.instance.emit('error');
    FakeEventSource.instance.emit('open');
    assert.equal(opens, 2);
});

console.log(`\n通过 ${pass} 项`);
