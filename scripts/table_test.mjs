// 校验 A5 的表格改动：配额 union bug、排序缓存、勾选唯一来源。
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

const { ResultTable, GROUP_DIMENSIONS } = await import('../web/js/table.js');

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

console.log('配额 union bug（A5 核心）:');
check('应用国家配额后可以取消其中一行', () => {
    const { t } = makeTable(SAMPLE);
    t.applyCountryQuotas({ 日本: 2 });
    assert.equal(t.getSelectedResults().length, 2);
    // 模拟用户取消勾选被配额选中的第一行
    const first = t.getSelectedResults()[0];
    t.selectedKeys.delete(ResultTable.keyOf(first));
    assert.equal(t.getSelectedResults().length, 1,
        '取消勾选无效——配额选择仍留在第二个集合里（union bug）');
});
check('配额是替换而非累加', () => {
    const { t } = makeTable(SAMPLE);
    t.applyCountryQuotas({ 日本: 2 });
    t.applyCountryQuotas({ 美国: 1 });
    const sel = t.getSelectedResults();
    assert.equal(sel.length, 1);
    assert.equal(sel[0].country, '美国');
});
check('clearSelection 清空全部勾选', () => {
    const { t } = makeTable(SAMPLE);
    t.applyCountryQuotas({ 日本: 3 });
    t.clearSelection();
    assert.equal(t.getSelectedResults().length, 0);
});
check('手动勾选与配额共用一个集合', () => {
    const { t } = makeTable(SAMPLE);
    t.selectedKeys.add(ResultTable.keyOf(SAMPLE[5])); // 手动勾新加坡
    t.applyCountryQuotas({ 日本: 1 });                // 配额会替换掉手动勾选
    const sel = t.getSelectedResults();
    assert.equal(sel.length, 1);
    assert.equal(sel[0].country, '日本');
});

console.log('配额语义：跟随筛选与排序:');
check('配额在筛选后的集合上取（改造前忽略筛选）', () => {
    const { t } = makeTable(SAMPLE);
    t.setFilter('日本');
    t.applyGroupQuota('country', 5);
    const sel = t.getSelectedResults();
    assert.equal(sel.length, 3, '筛选为日本时配额不该选到其他国家');
    assert.ok(sel.every(r => r.country === '日本'));
});
check('配额按当前排序取前 N（延迟升序 → 取最快）', () => {
    const { t } = makeTable(SAMPLE);
    t.applyGroupQuota('country', 1);
    const sel = t.getSelectedResults();
    assert.equal(sel.length, 3); // 三个国家各 1
    const jp = sel.find(r => r.country === '日本');
    assert.equal(jp.tcpLatencyMs, 10, '应取日本最快的那个');
});
check('每组取前 N：N 大于组内数量时取完', () => {
    const { t } = makeTable(SAMPLE);
    const n = t.applyGroupQuota('country', 10);
    assert.equal(n, 6);
});
check('N=0 或非法时不选任何行', () => {
    const { t } = makeTable(SAMPLE);
    t.applyGroupQuota('country', 0);
    assert.equal(t.getSelectedResults().length, 0);
    t.applyGroupQuota('country', -1);
    assert.equal(t.getSelectedResults().length, 0);
});
check('可按 ASN / 数据中心 / 出站类型分组', () => {
    const { t } = makeTable(SAMPLE);
    for (const d of GROUP_DIMENSIONS) {
        t.applyGroupQuota(d.key, 1);
        assert.ok(t.getSelectedResults().length >= 1, `${d.key} 分组失败`);
    }
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

console.log('分组统计:');
check('getGroupStats 按数量降序', () => {
    const { t } = makeTable(SAMPLE);
    const stats = t.getGroupStats('country');
    assert.equal(stats[0].name, '日本');
    assert.equal(stats[0].count, 3);
});
check('getCountryStats 兼容旧名', () => {
    const { t } = makeTable(SAMPLE);
    assert.deepEqual(t.getCountryStats(), t.getGroupStats('country'));
});
check('clear 重置结果与勾选', () => {
    const { t } = makeTable(SAMPLE);
    t.applyCountryQuotas({ 日本: 2 });
    t.clear();
    assert.equal(t.results.length, 0);
    assert.equal(t.getSelectedResults().length, 0);
});

console.log(`\n通过 ${pass} 项`);
