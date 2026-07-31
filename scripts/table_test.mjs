// 校验结果表格的排序、筛选、勾选、组合规则和虚拟滚动。
// ResultTable 依赖 DOM，这里用最小桩件模拟所需的 API。
import assert from 'node:assert/strict';

// ---- 最小 DOM 桩 ----
class El {
    constructor(tag = 'div') {
        this.tagName = tag.toUpperCase();
        this.children = [];
        this.dataset = {};
        this._html = '';
        this._listeners = {};
        this.classList = {
            _s: new Set(),
            add(c) { this._s.add(c); },
            remove(c) { this._s.delete(c); },
            toggle(c, on) { on ? this._s.add(c) : this._s.delete(c); },
            contains(c) { return this._s.has(c); },
        };
        this.checked = false;
        this.indeterminate = false;
        this.type = '';
        this.textContent = '';
    }
    get innerHTML() { return this._html; }
    set innerHTML(v) { this._html = v; }
    addEventListener(ev, fn) { (this._listeners[ev] ||= []).push(fn); }
    dispatchEvent(e) { (this._listeners[e.type] || []).forEach(fn => fn(e)); return true; }
    querySelector(sel) { return this._q.get(sel) ?? null; }
    querySelectorAll() { return []; }
    closest() { return new El('tr'); }
    _register(map) { this._q = map; }
}

globalThis.requestAnimationFrame = fn => fn();
globalThis.CustomEvent = class { constructor(type, opts = {}) { this.type = type; Object.assign(this, opts); } };

function makeContainer() {
    const container = new El();
    const tbody = new El('tbody');
    const selAll = new El('input');
    const map = new Map([['#result-tbody', tbody], ['#sel-all', selAll]]);
    container._register(map);
    container.querySelectorAll = () => [];
    return { container, tbody, selAll };
}

const { ResultTable } = await import('../web/js/table.js');

let pass = 0;
const check = (name, fn) => {
    try { fn(); pass++; console.log(`  ok  ${name}`); }
    catch (e) { console.log(`  FAIL ${name}\n       ${e.message}`); process.exitCode = 1; }
};

function makeTable(results) {
    const { container, tbody } = makeContainer();
    const t = new ResultTable(container);
    results.forEach(r => t.appendResult(r));
    return { t, tbody, container };
}

const R = (ip, country, latency, port = 443) =>
    ({ ip, port, country, tcpLatencyMs: latency, emoji: '🏳', dataCenter: 'XXX', asnOrg: 'CF', ipType: 'IPv4' });

const SAMPLE = [
    R('1.0.0.1', '日本', 10), R('1.0.0.2', '日本', 20), R('1.0.0.3', '日本', 30),
    R('2.0.0.1', '美国', 15), R('2.0.0.2', '美国', 25),
    R('3.0.0.1', '新加坡', 5),
];

// SAMPLE 的 dataCenter / asnOrg / ipType 全都一样，验证不了非国家维度的分组，
// 这里单独造一份各维度都有区分度的。
const MIXED = [
    { ...R('1.1.1.1', '日本', 10), dataCenter: 'NRT', asnOrg: 'Cloudflare, Inc.', ipType: 'IPv4' },
    { ...R('1.1.1.2', '日本', 20), dataCenter: 'NRT', asnOrg: 'Cloudflare, Inc.', ipType: 'IPv4' },
    { ...R('1.1.1.3', '美国', 30), dataCenter: 'LAX', asnOrg: 'Cloudflare, Inc.', ipType: 'IPv6' },
    { ...R('1.1.1.4', '美国', 40), dataCenter: 'LAX', asnOrg: 'Amazon', ipType: 'IPv6' },
    { ...R('1.1.1.5', '新加坡', 50), dataCenter: 'SIN', asnOrg: 'Amazon', ipType: 'IPv4' },
];

check('clearSelection 清空全部勾选', () => {
    const { t } = makeTable(SAMPLE);
    t.selectedKeys.add(ResultTable.keyOf(SAMPLE[0]));
    t.clearSelection();
    assert.equal(t.getSelectedResults().length, 0);
});
console.log('排序与缓存:');
check('排序缓存命中同一数组', () => {
    const { t } = makeTable(SAMPLE);
    const a = t._sortedResults();
    const b = t._sortedResults();
    assert.equal(a, b, '缓存未命中');
});
check('追加结果后缓存失效', () => {
    const { t } = makeTable(SAMPLE);
    const a = t._sortedResults();
    t.appendResult(R('9.9.9.9', '德国', 1));
    assert.notEqual(t._sortedResults(), a, '新结果未进入排序视图');
    assert.equal(t._sortedResults()[0].ip, '9.9.9.9');
});
check('测速回填后缓存失效', () => {
    const { t } = makeTable(SAMPLE);
    t.sortKey = 'downloadSpeedKBs'; t.sortAsc = false; t._invalidate();
    t.updateSpeed({ ip: '2.0.0.2', port: 443, downloadSpeedKBs: 5000 });
    assert.equal(t._sortedResults()[0].ip, '2.0.0.2', '回填后排序未更新');
});
check('getAllResults 返回筛选后的集合', () => {
    const { t } = makeTable(SAMPLE);
    t.setFilter('美国');
    assert.equal(t.getAllResults().length, 2);
});
check('getResults 返回忽略筛选的全部排序结果', () => {
    const { t } = makeTable(SAMPLE);
    t.setFilter('美国');
    assert.equal(t.getAllResults().length, 2);
    assert.equal(t.getResults().length, SAMPLE.length);
});

console.log('排序入口（setSort）:');
check('setSort 派发 sortchange，detail 带 key/asc', () => {
    const { t, container } = makeTable(SAMPLE);
    const seen = [];
    container.addEventListener('sortchange', e => seen.push(e.detail));
    t.setSort('ip', false);
    assert.equal(t.sortKey, 'ip');
    assert.equal(t.sortAsc, false);
    assert.deepEqual(seen, [{ key: 'ip', asc: false }],
        '下拉与方向按钮靠这个事件回填，不派发就会和表头点击脱节');
});

console.log('动态字段与结构化筛选:');
check('setColumns 保留勾选列、去重并忽略未知字段', () => {
    const { t } = makeTable(SAMPLE);
    t.setColumns(['country', 'ip', 'country', 'not-exists']);
    assert.deepEqual(t.columns.map(c => c.key), ['_sel', 'country', 'ip']);
});
check('国家、延迟与速度筛选可组合', () => {
    const rows = MIXED.map((r, i) => ({ ...r, downloadSpeedKBs: (i + 1) * 100 }));
    const { t } = makeTable(rows);
    t.setFilters({ country: '美国', maxLatency: 35, minSpeed: 250 });
    assert.deepEqual(t.getAllResults().map(r => r.ip), ['1.1.1.3']);
});
check('未测速结果不会通过最低速度筛选', () => {
    const { t } = makeTable([R('8.8.8.8', '美国', 12)]);
    t.setFilters({ minSpeed: 1 });
    assert.equal(t.getAllResults().length, 0);
});

console.log('展示前 N:');
check('组合规则支持国家+端口，并按当前排序取前 N', () => {
    const { t } = makeTable(SAMPLE);
    t.appendResult({ ip: '4.4.4.4', port: 2053, country: '日本', asnOrg: 'CF', tcpLatencyMs: 5 });
    const shown = t.applyDisplayRules([{ conditions: [
        { field: 'country', values: ['日本'] },
        { field: 'port', values: ['443'] },
    ], limit: 1 }]);
    assert.equal(shown, 1);
    assert.equal(t.getAllResults()[0].port, 443);
});
check('多条规则可按或合并', () => {
    const { t } = makeTable(SAMPLE);
    const shown = t.applyDisplayRules([
        { conditions: [{ field: 'country', values: ['日本'] }], limit: 1 },
        { conditions: [{ field: 'country', values: ['美国'] }], limit: 1 },
    ], 'or');
    assert.equal(shown, 2);
});
check('同一分组字段的多个值分别取前 N', () => {
    const { t } = makeTable(SAMPLE);
    const shown = t.applyDisplayRules([{ conditions: [{ field: 'country', values: ['日本', '美国'] }], limit: 1 }]);
    assert.equal(shown, 2);
    assert.deepEqual(new Set(t.getAllResults().map(r => r.country)), new Set(['日本', '美国']));
});
check('按当前筛选与排序限制每组展示数量', () => {
    const { t } = makeTable(SAMPLE);
    t.setFilters({ maxLatency: 25 });
    t.setSort('tcpLatencyMs', true);
    const shown = t.applyGroupDisplayQuotas('country', { 日本: 1, 美国: 1 });
    assert.equal(shown, 2);
    assert.deepEqual(t.getAllResults().map(r => r.tcpLatencyMs), [10, 15]);
});
check('展示前 N 会随排序变化重新计算', () => {
    const { t } = makeTable(SAMPLE);
    t.applyGroupDisplayQuotas('country', { 日本: 1 });
    assert.equal(t.getAllResults()[0].tcpLatencyMs, 10);
    t.setSort('tcpLatencyMs', false);
    assert.equal(t.getAllResults()[0].tcpLatencyMs, 30);
});
check('清除展示限制恢复当前筛选全集', () => {
    const { t } = makeTable(SAMPLE);
    t.applyGroupDisplayQuotas('country', { 日本: 1 });
    t.clearDisplayRules();
    assert.equal(t.getAllResults().length, SAMPLE.length);
});
check('分组统计可只统计当前筛选结果', () => {
    const { t } = makeTable(SAMPLE);
    t.setFilter('美国');
    assert.deepEqual(t.getGroupStats('country', { filtered: true }).map(s => s.name), ['美国']);
});
check('setSort 拒绝不可排序的列，且不派发事件', () => {
    const { t, container } = makeTable(SAMPLE);
    let fired = 0;
    container.addEventListener('sortchange', () => fired++);
    t.setSort('emoji');          // columns.js 里 sortable: false
    assert.equal(t.sortKey, 'tcpLatencyMs', '不该被改成不可排序的列');
    assert.equal(fired, 0);
});
check('setSort 换列后排序结果真的变了', () => {
    const { t } = makeTable(SAMPLE);
    t.setSort('tcpLatencyMs', true);
    assert.equal(t._sortedResults()[0].tcpLatencyMs, 5);
    t.setSort('tcpLatencyMs', false);
    assert.equal(t._sortedResults()[0].tcpLatencyMs, 30, '降序未生效（缓存没失效？）');
});

check('getGroupStats 只在国家维度给 emoji', () => {
    const { t } = makeTable(MIXED);
    assert.ok(t.getGroupStats('country')[0].emoji, '国家维度应带国旗');
    assert.equal(t.getGroupStats('asnOrg')[0].emoji, '',
        '按 ASN 分组时同组内国旗并不一致，带上会误导');
});

console.log('虚拟滚动窗口:');
check('行数低于阈值时渲染整个区间', () => {
    const { t } = makeTable(SAMPLE);
    assert.deepEqual(t._window(SAMPLE.length),
        { start: 0, end: SAMPLE.length, padTop: 0, padBottom: 0 });
});
check('没有 wrap 元素时不崩（桩 DOM 里 .table-wrap 取不到）', () => {
    const { t } = makeTable(SAMPLE);
    assert.equal(t.wrap, null, '桩件应当拿不到 .table-wrap');
    assert.deepEqual(t._window(10000), { start: 0, end: 10000, padTop: 0, padBottom: 0 },
        'wrap 为 null 时应退回全量区间，而不是拿 clientHeight 抛错');
});

console.log('分组统计:');
check('getGroupStats 按数量降序', () => {
    const { t } = makeTable(SAMPLE);
    const stats = t.getGroupStats('country');
    assert.equal(stats[0].name, '日本');
    assert.equal(stats[0].count, 3);
});
check('按国家统计', () => {
    const { t } = makeTable(SAMPLE);
    const stats = t.getGroupStats('country');
    assert.equal(stats.find(item => item.name === '日本')?.count, 3);
});
check('clear 重置结果与勾选', () => {
    const { t } = makeTable(SAMPLE);
    t.selectedKeys.add(ResultTable.keyOf(SAMPLE[0]));
    t.clear();
    assert.equal(t.results.length, 0);
    assert.equal(t.getSelectedResults().length, 0);
    assert.equal(t.displayRules, null);
});

console.log(`\n通过 ${pass} 项`);
